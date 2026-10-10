module github.com/oarkflow/ref/cmd/ref

go 1.26.5

require (
	github.com/oarkflow/ref v0.0.0
	github.com/oarkflow/ref/contrib/messaging v0.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.11.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oarkflow/authz v0.0.6 // indirect
	github.com/oarkflow/bcl v0.0.37 // indirect
	github.com/oarkflow/broker v0.0.0 // indirect
	github.com/oarkflow/convert v0.0.6 // indirect
	github.com/oarkflow/fh v0.0.26 // indirect
	github.com/oarkflow/interpreter v0.0.13 // indirect
	github.com/oarkflow/ip v0.0.11 // indirect
	github.com/oarkflow/phone v0.0.7 // indirect
	github.com/oarkflow/pkg v0.1.47 // indirect
	github.com/oarkflow/rules v0.0.5 // indirect
	github.com/oarkflow/smppflow v0.0.0-00010101000000-000000000000 // indirect
	github.com/oarkflow/spl v0.0.10 // indirect
	github.com/oarkflow/tcpguard v0.0.16 // indirect
	github.com/oarkflow/template v0.0.4 // indirect
	github.com/oarkflow/wuid v0.0.1 // indirect
	github.com/oarkflow/zlog v0.0.3 // indirect
	github.com/pierrec/lz4/v4 v4.1.29 // indirect
	github.com/redis/go-redis/v9 v9.22.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	go.etcd.io/raft/v3 v3.7.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.59.0 // indirect
)

replace (
	github.com/oarkflow/broker => ../../../broker
	github.com/oarkflow/ref => ../..
	github.com/oarkflow/ref/contrib/messaging => ../../contrib/messaging
	github.com/oarkflow/smppflow => ../../../smppflow
)
