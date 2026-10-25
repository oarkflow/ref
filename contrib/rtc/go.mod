module github.com/oarkflow/ref/contrib/rtc

go 1.27.3

require (
	github.com/oarkflow/fh v0.0.27
	github.com/oarkflow/ref v0.0.0-00010101000000-000000000000
	github.com/oarkflow/rtcd v0.0.1
	modernc.org/sqlite v1.61.0
)

require (
	github.com/at-wat/ebml-go v0.19.4 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/oarkflow/authz v0.0.6 // indirect
	github.com/oarkflow/bcl v0.0.37 // indirect
	github.com/oarkflow/convert v0.0.6 // indirect
	github.com/oarkflow/interpreter v0.0.15 // indirect
	github.com/oarkflow/ip v0.0.11 // indirect
	github.com/oarkflow/rules v0.0.5 // indirect
	github.com/oarkflow/tcpguard v0.0.16 // indirect
	github.com/oarkflow/wuid v0.0.1 // indirect
	github.com/pion/datachannel v1.6.3 // indirect
	github.com/pion/dtls/v3 v3.1.15 // indirect
	github.com/pion/ice/v4 v4.4.8 // indirect
	github.com/pion/interceptor v0.1.51 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.20 // indirect
	github.com/pion/rtp v1.10.6 // indirect
	github.com/pion/sctp v1.12.0 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/stun/v4 v4.0.1 // indirect
	github.com/pion/transport/v5 v5.1.1 // indirect
	github.com/pion/turn/v5 v5.1.2 // indirect
	github.com/pion/webrtc/v4 v4.2.25 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

replace github.com/oarkflow/ref => ../..

// rtcd's tests here start a real call service, so they build the SFU. A replace in a
// dependency is not inherited, so the pion/ice fork with make-before-break ICE
// restarts (see github.com/oarkflow/rtcd, README) is named again.
replace github.com/pion/ice/v4 => github.com/oarkflow/ice/v4 v4.4.8-oarkflow.1
