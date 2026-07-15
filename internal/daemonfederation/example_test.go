package daemonfederation_test

import (
	"fmt"

	"kitsoki/internal/daemonfederation"
)

func ExampleTunnel_SSHArgs() {
	tunnel := daemonfederation.Tunnel{
		Host: "worker.example", User: "kitsoki",
		LocalPort: 17777, RemoteHost: "127.0.0.1", RemotePort: 7777,
		IdentityFile: "/keys/worker", KnownHostsFile: "/keys/known_hosts",
	}
	args := tunnel.SSHArgs()
	fmt.Println(args[len(args)-3:])
	// Output:
	// [-L 127.0.0.1:17777:127.0.0.1:7777 kitsoki@worker.example]
}
