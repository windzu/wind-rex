module gorex

go 1.27.4

require (
	github.com/coder/websocket v1.8.15
	github.com/ebitengine/purego v0.11.3
	github.com/egoist/mygo v0.7.2
	github.com/quic-go/quic-go v0.62.0
	github.com/quic-go/webtransport-go v0.13.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/tailscale/peercred v0.0.0-20250107143737-35a0c7bd7edc
	github.com/yuin/gopher-lua v1.1.2
	golang.org/x/image v0.46.0
	golang.org/x/net v0.62.0
	golang.org/x/sys v0.50.0
	golang.org/x/term v0.48.0
	rsc.io/qr v0.2.0
)

require (
	github.com/ProtonMail/go-crypto v1.4.1 // indirect
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/dunglas/httpsfv v1.1.1 // indirect
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/ulikunitz/xz v0.5.16 // indirect
	go.digitalxero.dev/rpm v0.3.4 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/text v0.44.0 // indirect
)

tool github.com/egoist/mygo/cmd/mygo

replace github.com/egoist/mygo => github.com/si-mods/mygo v0.10.1
