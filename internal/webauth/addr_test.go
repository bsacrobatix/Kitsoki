package webauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsLoopbackAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:7777", true},
		{"localhost:7777", true},
		{"LOCALHOST:7777", true},
		{"[::1]:7777", true},
		{"127.0.0.2:7777", true}, // whole 127/8 is loopback
		{":7777", false},         // all interfaces
		{"0.0.0.0:7777", false},
		{"[::]:7777", false},
		{"192.168.1.5:7777", false},
		{"kitsoki.example.com:443", false}, // unresolvable name fails closed
		{"garbage", false},
		{"", false},
		{"localhost", true}, // bare host, no port
	}
	for _, c := range cases {
		assert.Equal(t, c.want, IsLoopbackAddr(c.addr), "addr %q", c.addr)
	}
}
