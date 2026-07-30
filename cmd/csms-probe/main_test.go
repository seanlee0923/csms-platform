package main

import "testing"

func TestProbePayloadsSupportEveryRuntimeVersion(t *testing.T) {
	for _, version := range []string{"ocpp1.6", "ocpp2.0.1", "ocpp2.1"} {
		t.Run(version, func(t *testing.T) {
			boot, status, err := probePayloads(version)
			if err != nil {
				t.Fatal(err)
			}
			if boot == nil || status == nil {
				t.Fatal("probe payload is nil")
			}
		})
	}
}

func TestProbePayloadsRejectUnsupportedVersion(t *testing.T) {
	if _, _, err := probePayloads("ocpp3.0"); err == nil {
		t.Fatal("unsupported version was accepted")
	}
}
