package donglereset

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestIsRoot(t *testing.T) {
	for id, want := range map[string]bool{
		`USB\VID_1532&PID_0555\O001000008`:           true,
		`usb\vid_1532&pid_0555\o001000008`:           true,
		`USB\VID_1532&PID_0555&MI_03\6&16979F78&0&3`: false,
		`HID\VID_1532&PID_0555&MI_03&COL02\7&3E7`:    false,
		`USB\VID_1532&PID_0555\`:                     false,
		`USB\VID_1532&PID_00B4\123`:                  false,
	} {
		if got := IsRoot(id); got != want {
			t.Errorf("IsRoot(%q) = %v", id, got)
		}
	}
}

func TestTaskXML(t *testing.T) {
	s := TaskXML("S-1-5-21-1-2-3-1001", `USB\VID_1532&PID_0555\O001000008`)
	var task struct {
		UserID    string `xml:"Principals>Principal>UserId"`
		LogonType string `xml:"Principals>Principal>LogonType"`
		Command   string `xml:"Actions>Exec>Command"`
		Args      string `xml:"Actions>Exec>Arguments"`
	}
	// encoding/xml refuses the UTF-16 declaration of a UTF-8 string; drop it.
	body := s[strings.Index(s, "\n")+1:]
	if err := xml.Unmarshal([]byte(body), &task); err != nil {
		t.Fatal(err)
	}
	if task.UserID != "S-1-5-21-1-2-3-1001" || task.LogonType != "S4U" {
		t.Errorf("principal: %+v", task)
	}
	want := `/d /s /c "net stop audiosrv /y & %SystemRoot%\System32\pnputil.exe /restart-device "USB\VID_1532&PID_0555\O001000008" & net start audiosrv"`
	if task.Command != `%SystemRoot%\System32\cmd.exe` || task.Args != want {
		t.Errorf("action: %q %q", task.Command, task.Args)
	}
}
