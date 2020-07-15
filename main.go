package main

import (
	"time"

	"github.com/hectodns/cachingproxy/cachingproxy"
	"github.com/hectodns/hectodns/hectocorn"
)

type config struct {
	To            []string `json:"to"`
	Except        []string `json:"except"`
	MaxConcurrent int64    `json:"max_concurrent"`
	Policy        string   `json:"policy"`
	Expire        string   `json:"expire"`
	TLSServerName string   `json:"tls_servername"`
	TLS           string   `json:"tls"`
	PreferUDP     bool     `json:"prefer_udp"`
	ForceTCP      bool     `json:"force_tcp"`
	ProbeTimeout  string   `json:"health_check"`
	MaxFails      int      `json:"max_fails"`
}

func main() {
	var (
		C   config
		err error
	)
	hectocorn.DecodeConfig(&C)

	f := cachingproxy.New()

	f.MaxConcurrent = C.MaxConcurrent
	f.MaxFails = uint32(C.MaxFails)

	f.ExpireTimeout, err = time.ParseDuration(C.Expire)
	if err != nil {
		hectocorn.Log.Error(err.Error())
		return
	}

	f.ProbeTimeout, err = time.ParseDuration(C.ProbeTimeout)
	if err != nil {
		hectocorn.Log.Error(err.Error())
		return
	}

	for _, to := range C.To {
		f.SetProxy(cachingproxy.NewProxy(to, "dns"))
	}

	hectocorn.ServeCore(f)
}
