package main

import (
	"github.com/hectodns/cachingproxy/cachingproxy"
	"github.com/hectodns/hectodns/hectocorn"
)

type config struct {
	To []string `json:"to"`
}

func main() {
	var C config
	hectocorn.DecodeConfig(&C)

	f := cachingproxy.New()

	for _, to := range C.To {
		f.SetProxy(cachingproxy.NewProxy(to, "dns"))
	}

	hectocorn.ServeCore(f)
}
