// Package donglereset restarts the headset dongle (USB re-enumeration, like
// re-plugging it). After some sleeps the dongle answers over HID but its radio
// never links with the headset again until it is re-plugged.
//
// Restarting a device needs administrator rights, the tray has none. So the
// feature is a Task Scheduler task created once with elevation (one UAC prompt):
// it runs system tools (cmd, net, pnputil) with a fixed device instance ID and
// the tray starts it on demand. No user-writable file is ever run elevated.
//
// audiodg.exe keeps the dongle's audio interface open and vetoes its removal:
// pnputil then restarts only the HID part, leaves the dongle "pending system
// reboot" and refuses every later restart until a reboot or a re-plug. So the
// task stops the Windows Audio service for the few seconds of the restart.
// Never pnputil /remove-device instead: the new device node gets new audio
// endpoints, and Equalizer APO on the mic and the apps' device choices are lost.
package donglereset

import (
	"bytes"
	"encoding/xml"
	"strings"
)

// TaskName is the scheduled task path.
const TaskName = `\bsctl\DongleReset`

// rootPrefix is the instance ID prefix of the dongle's USB composite device.
const rootPrefix = `USB\VID_1532&PID_0555\`

// IsRoot reports whether a device instance ID is the dongle itself (the composite
// device), not one of its interfaces (…&MI_xx).
func IsRoot(id string) bool {
	id = strings.ToUpper(id)
	return strings.HasPrefix(id, rootPrefix) && len(id) > len(rootPrefix)
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// Command is the task's command line for cmd.exe /d /s /c "…".
func Command(instanceID string) string {
	return `net stop audiosrv /y & %SystemRoot%\System32\pnputil.exe /restart-device "` + instanceID + `" & net start audiosrv`
}

// TaskXML is the task definition: no triggers, started only on demand, runs
// Command for instanceID as userSID with the highest rights without a window (S4U).
func TaskXML(userSID, instanceID string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>bsctl: перезапуск донгла Razer BlackShark (как переткнуть его). Запускает BlackSharkTray после сна, если гарнитура не подключилась.</Description>
  </RegistrationInfo>
  <Principals>
    <Principal id="Author">
      <UserId>` + esc(userSID) + `</UserId>
      <LogonType>S4U</LogonType>
      <RunLevel>HighestAvailable</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <ExecutionTimeLimit>PT1M</ExecutionTimeLimit>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%SystemRoot%\System32\cmd.exe</Command>
      <Arguments>/d /s /c "` + esc(Command(instanceID)) + `"</Arguments>
    </Exec>
  </Actions>
</Task>
`
}
