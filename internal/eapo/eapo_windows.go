//go:build windows

package eapo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/eyescreamxd/bsctl/internal/paths"
)

const (
	mmCaptureKey   = `SOFTWARE\Microsoft\Windows\CurrentVersion\MMDevices\Audio\Capture`
	pkeyIfaceName  = "{b3f8fa53-0004-438e-9003-51a46e139bfc},6" // "Razer BlackShark V2 Pro"
	pkeyDeviceDesc = "{a45c254e-df1c-4efd-8020-67d146a850e0},2" // "Microphone"
	pkeyFormat     = "{f19f064d-082c-4e27-bc73-6882a1bb8e4c},0" // WAVEFORMATEX of the shared mode
	fxPrefix       = "{d04e05a6-594b-4fb6-a80d-01af5eed7d1d},"
)

// Status is what the settings window and the log show.
type Status struct {
	Installed  bool
	InstallDir string
	ConfigDir  string
	Mic        *Mic
	Active     bool   // the plugin line is in the config and nothing blocks it
	Problem    string // why noise suppression cannot work right now
}

// EngineSource is the plugin next to the program.
func EngineSource(e Engine) string { return filepath.Join(paths.ExeDir(), e.Subdir, e.DLL) }

// VCRTMissing names the first Visual C++ runtime DLL missing in System32 ("" = all there).
func VCRTMissing() string {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return ""
	}
	for _, n := range []string{"msvcp140.dll", "vcruntime140.dll", "vcruntime140_1.dll"} {
		if _, err := os.Stat(filepath.Join(sys, n)); err != nil {
			return n
		}
	}
	return ""
}

var eapoMu sync.Mutex // one writer at a time (tray worker, settings window, CLI)

func Dirs() (install, config string, ok bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\EqualizerAPO`, registry.QUERY_VALUE)
	if err == nil {
		install, _, _ = k.GetStringValue("InstallPath")
		config, _, _ = k.GetStringValue("ConfigPath")
		k.Close()
	}
	if install == "" {
		install = filepath.Join(os.Getenv("ProgramFiles"), "EqualizerAPO")
	}
	if config == "" {
		config = filepath.Join(install, "config")
	}
	if st, err := os.Stat(config); err != nil || !st.IsDir() {
		return install, config, false
	}
	return install, config, true
}

func regStrings(k registry.Key, name string) []string {
	if s, _, err := k.GetStringValue(name); err == nil {
		return []string{s}
	}
	if ss, _, err := k.GetStringsValue(name); err == nil {
		return ss
	}
	return nil
}

// isEAPOClass: the CLSID belongs to EqualizerAPO.dll.
func isEAPOClass(clsid string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Classes\CLSID\`+clsid+`\InprocServer32`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("")
	return strings.Contains(strings.ToLower(v), "equalizerapo")
}

// HeadsetMics lists active capture endpoints of the BlackShark (not the Bluetooth one).
func HeadsetMics() ([]Mic, error) {
	root, err := registry.OpenKey(registry.LOCAL_MACHINE, mmCaptureKey, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	guids, err := root.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var out []Mic
	for _, g := range guids {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, mmCaptureKey+`\`+g, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		state, _, _ := k.GetIntegerValue("DeviceState")
		k.Close()
		if state != 1 {
			continue
		}
		p, err := registry.OpenKey(registry.LOCAL_MACHINE, mmCaptureKey+`\`+g+`\Properties`, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		m := Mic{GUID: g}
		m.Name, _, _ = p.GetStringValue(pkeyIfaceName)
		m.Connection, _, _ = p.GetStringValue(pkeyDeviceDesc)
		m.RegFormat, _, _ = p.GetBinaryValue(pkeyFormat)
		p.Close()
		low := strings.ToLower(m.Name)
		if !strings.Contains(low, "blackshark") || strings.HasSuffix(low, " bt") {
			continue
		}
		m.readFormat()
		if fx, err := registry.OpenKey(registry.LOCAL_MACHINE, mmCaptureKey+`\`+g+`\FxProperties`, registry.QUERY_VALUE); err == nil {
			names, _ := fx.ReadValueNames(-1)
			for _, n := range names {
				if !strings.HasPrefix(strings.ToLower(n), fxPrefix) {
					continue
				}
				for _, v := range regStrings(fx, n) {
					if strings.HasPrefix(v, "{") && isEAPOClass(v) {
						m.APOOnIt = true
					}
				}
			}
			fx.Close()
		}
		out = append(out, m)
	}
	return out, nil
}

// readFormat fills the device format: from the audio API, else from the registry copy.
func (m *Mic) readFormat() {
	f, err := captureDeviceFormat(m.GUID)
	if err == nil {
		m.Format, m.SampleRate, m.FormatFrom = f, f.Rate, "Windows Audio"
		return
	}
	if rf, ok := parseWaveFormat(m.RegFormat); ok {
		m.Format, m.SampleRate, m.FormatFrom = rf, rf.Rate, "реестр; Windows Audio: "+err.Error()
		return
	}
	m.FormatFrom = "не прочитан: " + err.Error()
}

func Detect() Status {
	var st Status
	st.InstallDir, st.ConfigDir, st.Installed = Dirs()
	mics, err := HeadsetMics()
	if err == nil && len(mics) > 0 {
		m := mics[0]
		for _, c := range mics {
			if c.APOOnIt {
				m = c
			}
		}
		st.Mic = &m
	}
	return st
}

// ProblemFor explains what blocks noise suppression (empty = nothing).
func (st Status) ProblemFor(nc NoiseConfig) string {
	switch {
	case !st.Installed:
		return "Equalizer APO не установлен"
	case st.Mic == nil:
		return "микрофон BlackShark не найден среди звуковых устройств (донгл воткнут?)"
	case !st.Mic.APOOnIt:
		return "Equalizer APO не подключён к микрофону BlackShark: открой Device Selector (DeviceSelector.exe, в старых версиях Configurator.exe), отметь его во вкладке Capture devices и перезагрузи ПК"
	}
	if !nc.Enabled {
		return ""
	}
	eng := FindEngine(nc.Engine)
	if eng.Need48k && st.Mic.SampleRate != 0 && st.Mic.SampleRate != 48000 {
		return fmt.Sprintf("микрофон работает на %d Гц, а %s нужно 48000 Гц: Параметры → Система → Звук → микрофон BlackShark → Формат → 48000 Гц", st.Mic.SampleRate, eng.Title)
	}
	if src := EngineSource(eng); FileMissing(src) {
		return "нет файла " + src
	}
	if eng.NeedVCRT {
		if n := VCRTMissing(); n != "" {
			return fmt.Sprintf("для %s нужен Microsoft Visual C++ Redistributable 2015–2022 (x64), в системе нет %s: скачай vc_redist.x64.exe с сайта Microsoft", eng.Title, n)
		}
	}
	return ""
}

func FileMissing(p string) bool {
	_, err := os.Stat(p)
	return err != nil
}

func writeIfChanged(path string, data []byte) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return false, nil
	}
	return true, os.WriteFile(path, data, 0o644)
}

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

// Apply writes bsctl.txt (and config.txt on first use) for the given settings;
// micEQ is the mic EQ curve, nil when flat.
func Apply(nc NoiseConfig, micEQ []int, lg Logf) (Status, error) {
	eapoMu.Lock()
	defer eapoMu.Unlock()
	st := Detect()
	st.Problem = st.ProblemFor(nc)
	if !st.Installed {
		return st, errors.New(st.Problem)
	}
	eng := FindEngine(nc.Engine)
	want := nc
	want.Engine = eng.Key
	switch {
	case st.Mic == nil || !st.Mic.APOOnIt:
		// nothing to target; the file stays valid and harmless
	case st.Problem != "" && nc.Enabled:
		want.Enabled = false // the plugin would not start or would garble the stream
	}
	if st.Mic == nil {
		want, micEQ = NoiseConfig{}, nil
	}
	dll := filepath.Join(st.ConfigDir, eapoDLLDir, eng.DLL)
	if want.Enabled {
		if err := installDLL(EngineSource(eng), dll); err != nil {
			st.Problem = err.Error()
			want.Enabled = false
		}
	}
	mic := Mic{}
	if st.Mic != nil {
		mic = *st.Mic
	}
	if changed, err := writeIfChanged(filepath.Join(st.ConfigDir, OwnFile), crlf(eapoOwnConfig(mic, want, micEQ, dll))); err != nil {
		return st, fmt.Errorf("запись %s: %w", OwnFile, err)
	} else if changed {
		what := "выключен"
		if want.Enabled {
			what = eng.Title
		}
		eq := "ровный"
		if micEQ != nil {
			eq = fmt.Sprint(micEQ)
		}
		why := ""
		if st.Problem != "" && (nc.Enabled || st.Mic == nil) {
			why = "; настройки из bsctl.json вернутся сами, пока: " + st.Problem
		}
		lg("Equalizer APO: %s обновлён (шумодав: %s, эквалайзер: %s, усиление %s дБ%s)", OwnFile, what, eq, TrimFloat(want.GainDB), why)
	}
	mainPath := filepath.Join(st.ConfigDir, "config.txt")
	cur, _ := os.ReadFile(mainPath)
	if next, write := nextMainConfig(string(cur)); write {
		if isStockEAPOConfig(string(cur)) {
			_ = os.WriteFile(mainPath+".bsctl-backup", cur, 0o644)
			lg("Equalizer APO: стандартный config.txt с демо-фильтрами (−6 дБ, подъём баса) заменён, копия — config.txt.bsctl-backup")
			next = string(crlf(next))
		} else {
			lg("Equalizer APO: в config.txt добавлена строка Include: %s", OwnFile)
		}
		if err := os.WriteFile(mainPath, []byte(next), 0o644); err != nil {
			return st, fmt.Errorf("запись config.txt: %w", err)
		}
	}
	if includesFile(eapoMainConfig(), eapoUserFile) {
		up := filepath.Join(st.ConfigDir, eapoUserFile)
		if _, err := os.Stat(up); os.IsNotExist(err) {
			_ = os.WriteFile(up, crlf("# Свои фильтры Equalizer APO. BlackSharkTray этот файл не трогает.\n"), 0o644)
		}
	}
	st.Active = want.Enabled
	return st, nil
}

// installDLL copies a plugin into the APO config folder (audiodg must be able to
// read it; the user profile is closed for the audio service).
func installDLL(src, dst string) error {
	if st, err := os.Stat(dst); err == nil {
		if ss, err := os.Stat(src); err == nil && ss.Size() == st.Size() && !ss.ModTime().After(st.ModTime()) {
			return nil // same size and not newer: already there (avoid reading 28 MB every minute)
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("нет %s рядом с программой", src)
	}
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("не скопировать %s в папку Equalizer APO: %v", filepath.Base(dst), err)
	}
	return nil
}

// Logf is a printf-like logger.
type Logf func(format string, args ...any)

// Uninstall removes what the tray put into Equalizer APO: the include line in
// config.txt, bsctl.txt and the copied plugins. user.txt and the backup of the
// stock config stay. A plugin in use by audiodg may stay until the next reboot.
func Uninstall(lg Logf) error {
	eapoMu.Lock()
	defer eapoMu.Unlock()
	_, dir, ok := Dirs()
	if !ok {
		lg("Equalizer APO не найден — убирать нечего")
		return nil
	}
	mainPath := filepath.Join(dir, "config.txt")
	if cur, err := os.ReadFile(mainPath); err == nil {
		if next, changed := removeOwnInclude(string(cur)); changed {
			if err := os.WriteFile(mainPath, []byte(next), 0o644); err != nil {
				return fmt.Errorf("запись config.txt: %w", err)
			}
			lg("Equalizer APO: строка Include: %s убрана из config.txt", OwnFile)
		}
	}
	if err := os.Remove(filepath.Join(dir, OwnFile)); err == nil {
		lg("Equalizer APO: %s удалён", OwnFile)
	}
	if err := os.RemoveAll(filepath.Join(dir, eapoDLLDir)); err != nil {
		lg("Equalizer APO: плагины в %s удалятся не все (заняты?): %v", filepath.Join(dir, eapoDLLDir), err)
	}
	return nil
}
