package cloud

import "strings"

const (
	PathDeviceRegister       = "/api/devices/register"
	PathDeviceGet            = "/api/devices/{deviceID}"
	PathDeviceHeartbeat      = "/api/devices/{deviceID}/heartbeat"
	PathDeviceTokenRotate    = "/api/devices/{deviceID}/token/rotate"
	PathDeviceCommandResult  = "/cloud/devices/{deviceID}/commands/{commandID}/result"
	PathGatewayAssign        = "/cloud/device/gateway-assign"
	PathUpdateCheck          = "/api/updates/check"
	PathAuthMe               = "/api/auth/me"
	PathOIDCToken            = "/oidc-auth/api/v1/plugin/login/token"
)

func DeviceGetPath(deviceID string) string {
	return strings.Replace(PathDeviceGet, "{deviceID}", deviceID, 1)
}

func DeviceHeartbeatPath(deviceID string) string {
	return strings.Replace(PathDeviceHeartbeat, "{deviceID}", deviceID, 1)
}

func DeviceTokenRotatePath(deviceID string) string {
	return strings.Replace(PathDeviceTokenRotate, "{deviceID}", deviceID, 1)
}

func DeviceCommandResultPath(deviceID, commandID string) string {
	p := strings.Replace(PathDeviceCommandResult, "{deviceID}", deviceID, 1)
	return strings.Replace(p, "{commandID}", commandID, 1)
}
