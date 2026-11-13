package core

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"time"
)

// On a phone there is no /etc/resolv.conf, and Go built without the Android
// toolchain cannot ask the system for names, so it would never find
// cloud-api.yandex.net. The Android app passes the phone's DNS servers in
// NOCTURNE_DNS; public ones are the fallback when the network has changed.
func init() {
	list := os.Getenv("NOCTURNE_DNS")
	if runtime.GOOS != "android" && list == "" {
		return
	}
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}
	var servers []string
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" && net.ParseIP(strings.SplitN(s, "%", 2)[0]) != nil {
			servers = append(servers, net.JoinHostPort(s, "53"))
		}
	}
	servers = append(servers, "77.88.8.8:53", "8.8.8.8:53", "1.1.1.1:53")
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			d := net.Dialer{Timeout: 3 * time.Second}
			var last error
			for _, s := range servers {
				c, err := d.DialContext(ctx, network, s)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
}
