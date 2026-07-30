package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	endpoint := flag.String("url", "", "OCPP WebSocket URL including station identity")
	version := flag.String("version", "ocpp1.6", "OCPP subprotocol: ocpp1.6, ocpp2.0.1 or ocpp2.1")
	hold := flag.Duration("hold", 0, "keep the WebSocket open after the OCPP flow")
	flag.Parse()
	if *endpoint == "" {
		log.Fatal("-url is required")
	}
	boot, status, err := probePayloads(*version)
	if err != nil {
		log.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{*version}, HandshakeTimeout: 10 * time.Second}
	connection, _, err := dialer.Dial(*endpoint, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer connection.Close()
	if connection.Subprotocol() != *version {
		log.Fatalf("server selected subprotocol %q, want %q", connection.Subprotocol(), *version)
	}
	if err := call(connection, "probe-boot", "BootNotification", boot); err != nil {
		log.Fatal(err)
	}
	if err := call(connection, "probe-status", "StatusNotification", status); err != nil {
		log.Fatal(err)
	}
	if *hold > 0 {
		if err := answerServerCalls(connection, *hold); err != nil {
			log.Fatal(err)
		}
	}
	fmt.Println("BootNotification and StatusNotification completed")
}

func probePayloads(version string) (any, any, error) {
	switch version {
	case "ocpp1.6":
		return map[string]any{
				"chargePointVendor": "CSMS", "chargePointModel": "Probe",
			}, map[string]any{
				"connectorId": 1, "errorCode": "NoError", "status": "Available",
			}, nil
	case "ocpp2.0.1", "ocpp2.1":
		return map[string]any{
				"reason":          "PowerUp",
				"chargingStation": map[string]any{"vendorName": "CSMS", "model": "Probe"},
			}, map[string]any{
				"timestamp":       time.Now().UTC().Format(time.RFC3339),
				"connectorStatus": "Available", "evseId": 1, "connectorId": 1,
			}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported OCPP subprotocol %q", version)
	}
}

func answerServerCalls(connection *websocket.Conn, hold time.Duration) error {
	if err := connection.SetReadDeadline(time.Now().Add(hold)); err != nil {
		return err
	}
	for {
		var frame []json.RawMessage
		if err := connection.ReadJSON(&frame); err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return nil
			}
			return err
		}
		if len(frame) != 4 || string(frame[0]) != "2" {
			return fmt.Errorf("unexpected server frame: %s", frame)
		}
		var id, action string
		if err := json.Unmarshal(frame[1], &id); err != nil {
			return err
		}
		if err := json.Unmarshal(frame[2], &action); err != nil {
			return err
		}
		if action != "Reset" {
			if err := connection.WriteJSON([]any{4, id, "NotImplemented", "unsupported action", map[string]any{}}); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("received Reset: %s\n", frame[3])
		if err := connection.WriteJSON([]any{3, id, map[string]any{"status": "Accepted"}}); err != nil {
			return err
		}
	}
}

func call(connection *websocket.Conn, id, action string, payload any) error {
	if err := connection.WriteJSON([]any{2, id, action, payload}); err != nil {
		return err
	}
	var response []json.RawMessage
	if err := connection.ReadJSON(&response); err != nil {
		return err
	}
	if len(response) != 3 || string(response[0]) != "3" || string(response[1]) != `"`+id+`"` {
		return fmt.Errorf("unexpected %s response: %s", action, response)
	}
	return nil
}
