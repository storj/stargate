module storj.io/gateway/testsuite

go 1.14

replace storj.io/gateway => ../

require (
	github.com/btcsuite/btcutil v1.0.1
	github.com/minio/cli v1.22.0
	github.com/minio/minio v0.0.0-20200306214424-88ae0f119610
	github.com/minio/minio-go/v6 v6.0.45
	github.com/stretchr/testify v1.4.0
	github.com/zeebo/errs v1.2.2
	go.uber.org/zap v1.10.0
	storj.io/common v0.0.0-20200319165559-1fc2508a7284
	storj.io/gateway v0.0.0-00010101000000-000000000000
	storj.io/storj v0.12.1-0.20200320110955-d994584e885e
	storj.io/uplink v1.0.0
)