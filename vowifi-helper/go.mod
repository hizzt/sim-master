module github.com/techblack/sim-master/vowifi-helper

go 1.26.3

require (
	github.com/1239t/swu-go v0.0.3
	github.com/1239t/vowifi-go v0.0.0
	github.com/jane-rui/vowifi-go v0.0.0-20260708060225-849dd11417bc
	github.com/warthog618/sms v0.3.0
)

replace github.com/1239t/vowifi-go => ./third_party/vowifi-go

replace github.com/jane-rui/vowifi-go => ./third_party/jane-vowifi-go

replace github.com/emiago/sipgo => ./third_party/vowifi-go/third_party/sipgo

require (
	github.com/emiago/sipgo v1.4.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/icholy/digest v1.1.0 // indirect
	github.com/iniwex5/netlink v1.3.3 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/rtp v1.10.2 // indirect
	github.com/pion/srtp/v3 v3.0.12 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/songgao/water v0.0.0-20200317203138-2b4b6d7c09d8 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.1 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	gvisor.dev/gvisor v0.0.0-20240521174809-5eedbf551134 // indirect
)
