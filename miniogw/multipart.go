// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

package miniogw

import (
	"context"
	"crypto/md5" /* #nosec G501 */ // Is only used for calculating a hash of the ETags of the all the parts of a multipart upload.
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"

	minio "github.com/storj/minio/cmd"
	"github.com/zeebo/errs"

	"storj.io/uplink"
	"storj.io/uplink/private/multipart"
	"storj.io/uplink/private/storage/streams"
)

func (layer *gatewayLayer) NewMultipartUpload(ctx context.Context, bucket, object string, opts minio.ObjectOptions) (uploadID string, err error) {
	defer mon.Task()(&ctx)(&err)

	// Scenario: if a client starts uploading an object and then dies, when
	// is it safe to restart uploading?
	// * with libuplink natively, it's immediately safe. the client died, so
	//   it stopped however far it got, and it can start over.
	// * with the gateway, unless we do the following line it is impossible
	//   to know when it's safe to start uploading again. it might be up to
	//   30 minutes later that it's safe! the reason is if the client goes
	//   away, the gateway keeps running, and may down the road decide the
	//   request was canceled, and so the object should get deleted.
	// So, to make clients of the gateway's behavior match libuplink, we are
	// disabling the cleanup on cancel that libuplink tries to do. we may
	// want to consider disabling this for libuplink entirely.
	// The following line currently only impacts UploadObject calls.
	ctx = streams.DisableDeleteOnCancel(ctx)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return "", err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	info, err := multipart.NewMultipartUpload(ctx, project, bucket, object, nil)
	if err != nil {
		return "", convertMultipartError(err, bucket, object, "")
	}
	return info.StreamID, nil
}

func (layer *gatewayLayer) GetMultipartInfo(ctx context.Context, bucket string, object string, uploadID string, opts minio.ObjectOptions) (info minio.MultipartInfo, err error) {
	info.Bucket = bucket
	info.Object = object
	info.UploadID = uploadID
	// TODO: We need an uplink API for this
	return info, nil
}

func (layer *gatewayLayer) PutObjectPart(ctx context.Context, bucket, object, uploadID string, partID int, data *minio.PutObjReader, opts minio.ObjectOptions) (info minio.PartInfo, err error) {
	defer mon.Task()(&ctx)(&err)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return minio.PartInfo{}, err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	partInfo, err := multipart.PutObjectPart(ctx, project, bucket, object, uploadID, partID-1, data)
	if err != nil {
		return minio.PartInfo{}, convertMultipartError(err, bucket, object, uploadID)
	}

	// TODO: Store the part's ETag in metabase

	return minio.PartInfo{
		PartNumber: partID,
		Size:       partInfo.Size,
		ETag:       data.MD5CurrentHexString(),
	}, nil
}

func (layer *gatewayLayer) AbortMultipartUpload(ctx context.Context, bucket, object, uploadID string, _ minio.ObjectOptions) (err error) {
	defer mon.Task()(&ctx)(&err)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	err = multipart.AbortMultipartUpload(ctx, project, bucket, object, uploadID)
	if err != nil {
		return convertMultipartError(err, bucket, object, uploadID)
	}
	return nil
}

func (layer *gatewayLayer) CompleteMultipartUpload(ctx context.Context, bucket, object, uploadID string, uploadedParts []minio.CompletePart, opts minio.ObjectOptions) (objInfo minio.ObjectInfo, err error) {
	defer mon.Task()(&ctx)(&err)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return minio.ObjectInfo{}, err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	// TODO: Check that ETag of uploadedParts match the ETags stored in metabase.

	etag, err := multipartUploadETag(uploadedParts)
	if err != nil {
		return minio.ObjectInfo{}, convertMultipartError(err, bucket, object, uploadID)
	}

	metadata := uplink.CustomMetadata(opts.UserDefined).Clone()
	metadata["s3:etag"] = etag

	obj, err := multipart.CompleteMultipartUpload(ctx, project, bucket, object, uploadID, &multipart.ObjectOptions{
		CustomMetadata: metadata,
	})
	if err != nil {
		return minio.ObjectInfo{}, convertMultipartError(err, bucket, object, uploadID)
	}

	return minioObjectInfo(bucket, etag, obj), nil
}

func (layer *gatewayLayer) ListObjectParts(ctx context.Context, bucket, object, uploadID string, partNumberMarker int, maxParts int, opts minio.ObjectOptions) (result minio.ListPartsInfo, err error) {
	defer mon.Task()(&ctx)(&err)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return minio.ListPartsInfo{}, err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	list, err := multipart.ListObjectParts(ctx, project, bucket, object, uploadID, partNumberMarker-1, maxParts)
	if err != nil {
		return minio.ListPartsInfo{}, convertMultipartError(err, bucket, object, uploadID)
	}

	parts := make([]minio.PartInfo, 0, len(list.Items))
	for _, item := range list.Items {
		parts = append(parts, minio.PartInfo{
			PartNumber:   item.PartNumber + 1,
			LastModified: item.LastModified,
			ETag:         "",        // TODO: Entity tag returned when the part was initially uploaded.
			Size:         item.Size, // Size in bytes of the part.
			ActualSize:   item.Size, // Decompressed Size.
		})
	}
	sort.Slice(parts, func(i, k int) bool {
		return parts[i].PartNumber < parts[k].PartNumber
	})
	return minio.ListPartsInfo{
		Bucket:               bucket,
		Object:               object,
		UploadID:             uploadID,
		StorageClass:         "",               // TODO
		PartNumberMarker:     partNumberMarker, // Part number after which listing begins.
		NextPartNumberMarker: partNumberMarker, // TODO Next part number marker to be used if list is truncated
		MaxParts:             maxParts,
		IsTruncated:          list.More,
		Parts:                parts,
		// also available: UserDefined map[string]string
	}, nil
}

// ListMultipartUploads lists all multipart uploads.
func (layer *gatewayLayer) ListMultipartUploads(ctx context.Context, bucket string, prefix string, keyMarker string, uploadIDMarker string, delimiter string, maxUploads int) (result minio.ListMultipartsInfo, err error) {
	defer mon.Task()(&ctx)(&err)

	project, err := layer.openProjectMultipart(ctx, getAccessGrant(ctx))
	if err != nil {
		return minio.ListMultipartsInfo{}, err
	}
	defer func() {
		err = errs.Combine(err, project.Close())
	}()

	// TODO maybe this should be checked by project.ListMultipartUploads
	if bucket == "" {
		return minio.ListMultipartsInfo{}, minio.BucketNameInvalid{}
	}

	if delimiter != "" && delimiter != "/" {
		return minio.ListMultipartsInfo{}, minio.UnsupportedDelimiter{Delimiter: delimiter}
	}

	// TODO this should be removed and implemented on satellite side
	defer func() {
		err = checkBucketError(ctx, project, bucket, "", err)
	}()
	recursive := delimiter == ""

	var list *multipart.UploadIterator

	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		list = multipart.ListPendingObjectStreams(ctx, project, bucket, prefix, &multipart.ListMultipartUploadsOptions{
			System: true,
			Custom: true,
		})
	} else {
		list = multipart.ListMultipartUploads(ctx, project, bucket, &multipart.ListMultipartUploadsOptions{
			Prefix:    prefix,
			Cursor:    keyMarker,
			Recursive: recursive,
			System:    true,
			Custom:    true,
		})
	}

	startAfter := keyMarker
	var uploads []minio.MultipartInfo
	var prefixes []string

	limit := maxUploads
	for (limit > 0 || maxUploads == 0) && list.Next() {
		limit--
		object := list.Item()
		if object.IsPrefix {
			prefixes = append(prefixes, object.Key)
			continue
		}

		uploads = append(uploads, minioMultipartInfo(bucket, object))

		startAfter = object.Key

	}
	if list.Err() != nil {
		return result, convertMultipartError(list.Err(), bucket, "", "")
	}

	more := list.Next()
	if list.Err() != nil {
		return result, convertMultipartError(list.Err(), bucket, "", "")
	}

	result = minio.ListMultipartsInfo{
		KeyMarker:      keyMarker,
		UploadIDMarker: uploadIDMarker,
		MaxUploads:     maxUploads,
		IsTruncated:    more,
		Uploads:        uploads,
		Prefix:         prefix,
		Delimiter:      delimiter,
		CommonPrefixes: prefixes,
	}
	if more {
		result.NextKeyMarker = startAfter
		// TODO: NextUploadID
	}

	return result, nil
}

func minioMultipartInfo(bucket string, object *multipart.Object) minio.MultipartInfo {
	if object == nil {
		object = &multipart.Object{}
	}

	return minio.MultipartInfo{
		Bucket:      bucket,
		Object:      object.Key,
		Initiated:   object.System.Created,
		UploadID:    object.StreamID,
		UserDefined: object.Custom,
	}
}

func multipartUploadETag(parts []minio.CompletePart) (string, error) {
	var hashes []byte
	for _, part := range parts {
		md5, err := hex.DecodeString(canonicalEtag(part.ETag))
		if err != nil {
			hashes = append(hashes, []byte(part.ETag)...)
		} else {
			hashes = append(hashes, md5...)
		}
	}

	/* #nosec G401 */ // ETags aren't security sensitive
	sum := md5.Sum(hashes)
	return hex.EncodeToString(sum[:]) + "-" + strconv.Itoa(len(parts)), nil
}

func canonicalEtag(etag string) string {
	etag = strings.Trim(etag, `"`)
	p := strings.IndexByte(etag, '-')
	if p >= 0 {
		return etag[:p]
	}
	return etag
}

func convertMultipartError(err error, bucket, object, uploadID string) error {
	if errors.Is(err, multipart.ErrStreamIDInvalid) {
		return minio.InvalidUploadID{Bucket: bucket, Object: object, UploadID: uploadID}
	}

	return convertError(err, bucket, object)
}
