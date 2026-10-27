module github.com/oarkflow/ref/cmd/ref

go 1.27.3

require (
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.11.0
	github.com/oarkflow/ref v0.0.0
	github.com/oarkflow/ref/contrib/messaging v0.0.0
	github.com/oarkflow/ref/contrib/rtc v0.0.0
	modernc.org/sqlite v1.61.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/brianvoe/gofakeit/v6 v6.28.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/goccy/go-reflect v1.2.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gookit/color v1.6.3 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/klauspost/compress v1.20.3 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/oarkflow/authz v0.0.6 // indirect
	github.com/oarkflow/bcl v0.0.37 // indirect
	github.com/oarkflow/broker v0.0.1 // indirect
	github.com/oarkflow/cli v0.0.3 // indirect
	github.com/oarkflow/config v0.0.1 // indirect
	github.com/oarkflow/convert v0.0.6 // indirect
	github.com/oarkflow/date v0.0.5 // indirect
	github.com/oarkflow/expr v0.0.11 // indirect
	github.com/oarkflow/fh v0.0.27 // indirect
	github.com/oarkflow/interpreter v0.0.15 // indirect
	github.com/oarkflow/ip v0.0.11 // indirect
	github.com/oarkflow/jet v0.0.4 // indirect
	github.com/oarkflow/json v0.0.28 // indirect
	github.com/oarkflow/migrate v0.0.30 // indirect
	github.com/oarkflow/phone v0.0.7 // indirect
	github.com/oarkflow/pkg v0.1.47 // indirect
	github.com/oarkflow/rtcd v0.0.1 // indirect
	github.com/oarkflow/rules v0.0.5 // indirect
	github.com/oarkflow/smppflow v0.0.1 // indirect
	github.com/oarkflow/spl v0.0.10 // indirect
	github.com/oarkflow/squealx v0.0.81 // indirect
	github.com/oarkflow/squealx/drivers/mysql v0.0.81 // indirect
	github.com/oarkflow/squealx/drivers/postgres v0.0.81 // indirect
	github.com/oarkflow/squealx/drivers/sqlite v0.0.81 // indirect
	github.com/oarkflow/tcpguard v0.0.16 // indirect
	github.com/oarkflow/template v0.0.4 // indirect
	github.com/oarkflow/wuid v0.0.1 // indirect
	github.com/oarkflow/zlog v0.0.4 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/urfave/cli/v3 v3.14.0 // indirect
	github.com/xo/terminfo v1.2.0 // indirect
	go.etcd.io/raft/v3 v3.7.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261012220534-4341edf3f941 // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// interpreter's Windows fix (v0.0.15) and zlog's (v0.0.4) are both published
// upstream now; no local replace is needed for either.
replace (
	github.com/oarkflow/ref => ../..
	github.com/oarkflow/ref/contrib/messaging => ../../contrib/messaging
	github.com/oarkflow/ref/contrib/rtc => ../../contrib/rtc
)
