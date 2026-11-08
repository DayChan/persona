package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- kind service's credentials (service.go there) ----

// connectorCred is a service machine's pin and credential, opened for the manager.
func (h *Hub) connectorCred(ctx context.Context, machine string) (devicewire.ConnectorCred, error) {
	cs := h.centerStore()
	if cs == nil {
		return devicewire.ConnectorCred{}, errors.New("no database")
	}
	row, err := cs.ConnectorCredential(ctx, machine)
	if err != nil {
		return devicewire.ConnectorCred{}, err
	}
	if row == nil {
		return devicewire.ConnectorCred{MachineID: machine}, nil
	}
	tok, err := h.openKey(row.Token)
	if err != nil {
		return devicewire.ConnectorCred{}, fmt.Errorf("读不出连接器的凭据：%w", err)
	}
	return devicewire.ConnectorCred{MachineID: machine, FP: row.FP, Token: tok, Version: row.Version}, nil
}

// keepConnectorCred seals and keeps what pairing made.
func (h *Hub) keepConnectorCred(ctx context.Context, c devicewire.ConnectorCred) error {
	cs := h.centerStore()
	if cs == nil {
		return errors.New("no database")
	}
	if mc := h.knownEntry(c.MachineID); mc == nil || mc.Kind != kindService {
		// Pairing for an entry being added: it is written to hub.json right after.
		if mc != nil {
			return fmt.Errorf("%s is not of kind service", c.MachineID)
		}
	}
	sealed, err := h.sealKey(c.Token)
	if err != nil {
		return err
	}
	return cs.SaveConnectorCredential(ctx, db.ConnectorCredential{MachineID: c.MachineID, FP: c.FP, Token: sealed,
		Version: clip(oneLine(c.Version), 80)})
}

// pairConnector has the machine's manager (the carrier for one being added) pair a service machine's connector (the
// admin page); it keeps the credential (devices.connector_paired, its own machine's only).
func (h *Hub) pairConnector(ctx context.Context, pairLink, id, address, proxyURL string) (string, string, error) {
	_ = h.waitDevices(ctx) // the hub just started: the manager is on its way
	var out struct {
		Address string `json:"address"`
		Version string `json:"version"`
	}
	err := h.callBus(ctx, h.machineOwner(id), devicewire.MethodPairConnector, devicewire.PairConnector{Link: pairLink, MachineID: id,
		Address: address, Proxy: proxyURL}, &out)
	if errors.Is(err, errBusOffline) {
		return "", "", errAssistantOffline
	}
	if err != nil {
		return "", "", err
	}
	return out.Address, out.Version, nil
}

const kindService = "service"

// validProxy checks a socks5:// proxy for a service machine (the manager dials through it).
func validProxy(proxyURL string) error {
	if proxyURL == "" {
		return nil
	}
	if !strings.HasPrefix(proxyURL, "socks5://") && !strings.HasPrefix(proxyURL, "socks5h://") ||
		strings.TrimPrefix(strings.TrimPrefix(proxyURL, "socks5h://"), "socks5://") == "" {
		return i18n.Err("hub-devices_link.bad_proxy")
	}
	return nil
}
