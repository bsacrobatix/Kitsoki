package main

import "testing"

func TestQueueAdmissionPlaintextListenIsLoopbackOnly(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7444", "[::1]:7444", "localhost:7444"} {
		if !loopbackListen(address) {
			t.Errorf("loopbackListen(%q) = false", address)
		}
	}
	for _, address := range []string{"0.0.0.0:7444", "[::]:7444", "10.0.0.2:7444", "bad"} {
		if loopbackListen(address) {
			t.Errorf("loopbackListen(%q) = true", address)
		}
	}
}
