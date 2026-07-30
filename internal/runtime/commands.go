package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/seanlee0923/csms-platform/internal/commandbus"
	"github.com/seanlee0923/ocpp/csms"
	"github.com/seanlee0923/ocpp/protocol"
	"github.com/seanlee0923/ocpp/v16"
	"github.com/seanlee0923/ocpp/v201"
	"github.com/seanlee0923/ocpp/v21"
)

const resetAction = "Reset"

type resetCommand struct {
	Type   string `json:"type"`
	EVSEID *int   `json:"evseId,omitempty"`
}

func (s *Server) handleCommand(ctx context.Context, command commandbus.Command) commandbus.Result {
	fail := func(code string, err error) commandbus.Result {
		return commandbus.Result{Success: false, ErrorCode: code, ErrorDescription: err.Error()}
	}
	if ctx.Err() != nil {
		return fail("Expired", ctx.Err())
	}
	if s.ownership == nil || command.OwnerID != s.ownership.ownerID ||
		!s.ownership.owns(command.StationIdentity, command.OwnerGeneration) {
		return fail("NotOwner", fmt.Errorf("station ownership changed"))
	}
	session, ok := s.ocpp.Session(command.StationIdentity)
	if !ok {
		return fail("StationNotConnected", fmt.Errorf("station is not connected to this runtime"))
	}
	if command.Action != resetAction {
		return fail("UnsupportedAction", fmt.Errorf("unsupported command action %q", command.Action))
	}
	var request resetCommand
	if err := json.Unmarshal(command.Payload, &request); err != nil {
		return fail("InvalidPayload", err)
	}
	payload, err := s.callReset(ctx, session.Version(), session, request)
	if err != nil {
		return fail("OCPPCallFailed", err)
	}
	return commandbus.Result{Success: true, Payload: payload}
}

func (s *Server) callReset(ctx context.Context, version protocol.Version, session *csms.Session, request resetCommand) (json.RawMessage, error) {
	var confirmation any
	var err error
	switch version {
	case protocol.OCPP16:
		resetType := v16.ResetRequestTypeHard
		if request.Type == "OnIdle" {
			resetType = v16.ResetRequestTypeSoft
		} else if request.Type != "Immediate" {
			return nil, fmt.Errorf("reset type must be Immediate or OnIdle")
		}
		confirmation, err = s.profiles.OCPP16.CallReset(ctx, session, v16.ResetRequest{Type: resetType})
	case protocol.OCPP201:
		if request.Type != "Immediate" && request.Type != "OnIdle" {
			return nil, fmt.Errorf("reset type must be Immediate or OnIdle")
		}
		confirmation, err = s.profiles.OCPP201.CallReset(ctx, session, v201.ResetRequest{
			Type: v201.ResetRequestResetEnum(request.Type), EVSEID: request.EVSEID,
		})
	case protocol.OCPP21:
		if request.Type != "Immediate" && request.Type != "OnIdle" {
			return nil, fmt.Errorf("reset type must be Immediate or OnIdle")
		}
		confirmation, err = s.profiles.OCPP21.CallReset(ctx, session, v21.ResetRequest{
			Type: v21.ResetRequestResetEnum(request.Type), EVSEID: request.EVSEID,
		})
	default:
		return nil, fmt.Errorf("unsupported OCPP version %q", version)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(confirmation)
}
