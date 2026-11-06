//go:build windows

package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyescreamxd/bsctl/internal/audio"
	"github.com/eyescreamxd/bsctl/internal/autostart"
	"github.com/eyescreamxd/bsctl/internal/config"
	"github.com/eyescreamxd/bsctl/internal/eapo"
	"github.com/eyescreamxd/bsctl/internal/headset"
	"github.com/eyescreamxd/bsctl/internal/hid"
	"github.com/eyescreamxd/bsctl/internal/razer"
	"github.com/eyescreamxd/bsctl/internal/tray"
	"github.com/eyescreamxd/bsctl/internal/version"
)

// defaultMode is set to "tray" for the GUI build via -ldflags "-X main.defaultMode=tray".
var defaultMode = ""

const usage = `bsctl %s — Razer BlackShark V2 Pro (2023), Basilisk V3 Pro и Mouse Dock Pro без Synapse

Гарнитура:
  bsctl probe              проверить связь с донглом (только чтение) и записать подробный лог
  bsctl status             подключение, заряд, микрофон, серийник, прошивка донгла
  bsctl apply              отправить в гарнитуру настройки из bsctl.json
  bsctl eq <пресет>        custom | autoeq | game | movie | music | apex | csgo | valorant | fortnite | cod
  bsctl eq 0,0,1,2,...     свой EQ: 10 полос в дБ (31,63,125,250,500,1k,2k,4k,8k,16k), -12..12
  bsctl sidetone on|off [N] самопрослушивание, N — громкость 0-100 (по умолчанию 50)
  bsctl monitor [сек]      записать в лог все события от гарнитуры (диагностика)
  bsctl miceq <пресет>     эквалайзер микрофона через Equalizer APO: default | micboost | broadcast | conference
  bsctl miceq 0,0,1,...    свой эквалайзер микрофона, 10 полос в дБ (через Equalizer APO)

Шумоподавление (Equalizer APO: RNNoise или DeepFilterNet):
  bsctl eapo               состояние: APO, микрофон, частота, конфиг (только чтение)
  bsctl ns on|off [дБ]     включить/выключить шумодав на микрофоне, дБ — доп. усиление
  bsctl ns rnnoise|dfn     включить шумодав RNNoise или DeepFilterNet

Мышь и док:
  bsctl mouse              заряд мыши, прошивки, коллекции HID (только чтение, подробный лог)
  bsctl mouse-monitor [сек] записать события дока (заряд, зарядка, сон мыши)
  bsctl dock test          прогнать цвета дока: зелёный, жёлтый, оранжевый, красный, дыхание, радуга
  bsctl dock battery       покрасить док по заряду мыши (как трей)
  bsctl dock RRGGBB        статичный цвет, например 00FF00;  bsctl dock breath RRGGBB — дыхание
  bsctl dock spectrum|off  радуга / выключить
  bsctl dock bright <0-100> яркость дока
  bsctl dock mode 0|3      режим дока: 0 обычный, 3 как при Synapse (диагностика)

  bsctl tray               значки в трее (то же, что BlackSharkTray.exe)
  bsctl autostart on|off   запуск трея при входе в Windows

Настройки: bsctl.json, лог: bsctl.log — рядом с exe.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 && defaultMode != "" {
		args = []string{defaultMode}
	}
	if len(args) == 0 {
		fmt.Printf(usage, version.Version)
		return
	}
	if args[0] == "tray" {
		tray.Run()
		return
	}
	if args[0] == "tray-selftest" {
		tray.RunSelfTest()
		return
	}
	if args[0] == "mic-hold" { // diagnostics: keep the headset mic recording for N seconds
		secs := 30
		if len(args) > 1 {
			fmt.Sscan(args[1], &secs)
		}
		m, err := audio.OpenMicHold()
		if err != nil {
			fmt.Println("ОШИБКА:", err)
			os.Exit(1)
		}
		fmt.Printf("микрофон %q открыт на %d с — говори, должен слышать себя (если самопрослушивание включено)\n", m.Name, secs)
		time.Sleep(time.Duration(secs) * time.Second)
		m.Close()
		fmt.Printf("готово, записано буферов по 100 мс: %d\n", m.Bufs)
		return
	}
	lg := config.NewLogger(true)
	cfg, err := config.Load()
	if err != nil {
		lg.Printf("ошибка в bsctl.json: %v (использую значения по умолчанию)", err)
	}
	if err := runCLI(args, cfg, lg); err != nil {
		lg.Printf("ОШИБКА: %v", err)
		os.Exit(1)
	}
}

func runCLI(args []string, cfg *config.Config, lg *log.Logger) error {
	switch args[0] {
	case "probe":
		return probe(cfg, lg)
	case "status":
		hs, err := openHeadsetCLI(cfg, lg)
		if err != nil {
			return err
		}
		defer hs.Close()
		printStatus(hs, lg)
		return nil
	case "apply":
		return withHeadset(cfg, lg, func(hs *headset.Headset) error {
			hs.Verbose = true
			return hs.Apply(cfg.HeadsetSettings())
		})
	case "monitor":
		return monitor(cfg, lg, argSeconds(args, 90))
	case "eq":
		if len(args) < 2 {
			return fmt.Errorf("укажи пресет или 10 значений")
		}
		if p, ok := headset.FindPreset(args[1]); ok {
			cfg.Preset = p.Key
		} else {
			bands, err := headset.ParseBands(args[1])
			if err != nil {
				return err
			}
			cfg.Preset, cfg.CustomEQ = "custom", bands
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		g, i, b := cfg.EffectiveEQ()
		return withHeadset(cfg, lg, func(hs *headset.Headset) error {
			if err := hs.SetEQ(g, i, b); err != nil {
				return err
			}
			lg.Printf("EQ: %s %v", cfg.Preset, b)
			return nil
		})
	case "sidetone":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return fmt.Errorf("bsctl sidetone on|off [громкость 0-100]")
		}
		cfg.Sidetone = args[1] == "on"
		if len(args) > 2 {
			if _, err := fmt.Sscan(args[2], &cfg.SidetoneLevel); err != nil || cfg.SidetoneLevel < 0 || cfg.SidetoneLevel > 100 {
				return fmt.Errorf("громкость самопрослушивания 0..100")
			}
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		return withHeadset(cfg, lg, func(hs *headset.Headset) error {
			hs.Verbose = true
			if err := hs.SetSidetone(cfg.Sidetone, cfg.SidetoneLevel); err != nil {
				return err
			}
			if on, lvl, err := hs.Sidetone(); err == nil {
				lg.Printf("самопрослушивание в гарнитуре: включено=%v, громкость %d%%", on, lvl)
			}
			return nil
		})
	case "miceq":
		if len(args) < 2 {
			return fmt.Errorf("bsctl miceq default|micboost|broadcast|conference или 10 значений через запятую")
		}
		if p, ok := headset.FindMicPresetKey(args[1]); ok && p.Bands != nil {
			cfg.MicPreset = int(p.Index)
		} else {
			bands, err := headset.ParseBands(args[1])
			if err != nil {
				return err
			}
			cfg.MicPreset, cfg.MicCustomEQ = headset.MicCustomIndex, bands
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		st, err := eapo.Apply(cfg.Noise, cfg.MicEQ(), lg.Printf)
		if err != nil {
			return err
		}
		if p := st.ProblemFor(eapo.NoiseConfig{}); p != "" {
			return fmt.Errorf("эквалайзер микрофона работает через Equalizer APO: %s", p)
		}
		lg.Printf("эквалайзер микрофона (Equalizer APO): %v", cfg.MicEQ())
		return nil
	case "eapo":
		return eapoInfo(cfg, lg)
	case "ns":
		if len(args) < 2 {
			return fmt.Errorf("bsctl ns on|off|rnnoise|dfn [усиление дБ]")
		}
		switch args[1] {
		case "on", "off":
			cfg.Noise.Enabled = args[1] == "on"
		case "rnnoise", "deepfilternet", "dfn":
			cfg.Noise.Enabled, cfg.Noise.Engine = true, eapo.FindEngine(strings.Replace(args[1], "dfn", "deepfilternet", 1)).Key
		default:
			return fmt.Errorf("bsctl ns on|off|rnnoise|dfn [усиление дБ]")
		}
		if len(args) > 2 {
			if _, err := fmt.Sscan(args[2], &cfg.Noise.GainDB); err != nil {
				return fmt.Errorf("усиление в дБ, например 6")
			}
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		st, err := eapo.Apply(cfg.Noise, cfg.MicEQ(), lg.Printf)
		if err != nil {
			return err
		}
		if p := st.ProblemFor(cfg.Noise); p != "" {
			lg.Printf("внимание: %s", p)
		}
		lg.Printf("шумоподавление: %v (%s), усиление %s дБ", st.Active, eapo.FindEngine(cfg.Noise.Engine).Title, eapo.TrimFloat(cfg.Noise.GainDB))
		return nil
	case "mouse":
		return mouseInfo(lg)
	case "mouse-monitor":
		return mouseMonitor(lg, argSeconds(args, 60))
	case "dock":
		return dockCmd(args[1:], cfg, lg)
	case "autostart":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return fmt.Errorf("bsctl autostart on|off")
		}
		if err := autostart.Set(args[1] == "on"); err != nil {
			return err
		}
		lg.Printf("автозапуск: %s", args[1])
		return nil
	default:
		fmt.Printf(usage, version.Version)
		return nil
	}
}

func argSeconds(args []string, def int) time.Duration {
	secs := def
	if len(args) > 1 {
		fmt.Sscan(args[1], &secs)
	}
	return time.Duration(secs) * time.Second
}

func warnSynapse(lg *log.Logger) {
	if hid.ProcessRunning("RazerAppEngine.exe") {
		lg.Printf("внимание: запущен Synapse (RazerAppEngine.exe) — он может перебить настройки. Для чистой проверки выйди из него в трее.")
	}
}

// openHeadsetCLI opens the dongle and remembers the working collection in bsctl.json.
func openHeadsetCLI(cfg *config.Config, lg *log.Logger) (*headset.Headset, error) {
	path, setOut := cfg.DevicePath, cfg.UseSetOutput
	conn := cfg.HeadsetConn()
	hs, err := headset.Open(&conn, lg)
	cfg.DevicePath, cfg.UseSetOutput = conn.DevicePath, conn.UseSetOutput
	if err == nil && (cfg.DevicePath != path || cfg.UseSetOutput != setOut) {
		_ = config.Save(cfg)
	}
	return hs, err
}

func withHeadset(cfg *config.Config, lg *log.Logger, f func(*headset.Headset) error) error {
	warnSynapse(lg)
	hs, err := openHeadsetCLI(cfg, lg)
	if err != nil {
		return err
	}
	defer hs.Close()
	if err := f(hs); err != nil {
		return err
	}
	lg.Printf("готово")
	return nil
}

func printStatus(hs *headset.Headset, lg *log.Logger) {
	if c, err := hs.Connected(); err == nil {
		lg.Printf("гарнитура: %s", map[bool]string{true: "подключена", false: "не подключена к донглу"}[c])
	} else {
		lg.Printf("подключение: %v", err)
	}
	if b, err := hs.Battery(); err == nil {
		lg.Printf("заряд: %d%%", b)
	} else {
		lg.Printf("заряд: %v", err)
	}
	if ch, err := hs.Charging(); err == nil {
		lg.Printf("заряжается: %v", ch)
	}
	if m, err := hs.MicMuted(); err == nil {
		lg.Printf("микрофон выключен: %v", m)
	} else {
		lg.Printf("состояние микрофона: %v (не страшно — трей узнает его по кнопке)", err)
	}
	if on, lvl, err := hs.Sidetone(); err == nil {
		lg.Printf("самопрослушивание: включено=%v, громкость %d%%", on, lvl)
	} else {
		lg.Printf("самопрослушивание: %v", err)
	}
	if st, err := hs.EQStatus(); err == nil {
		lg.Printf("флаг speaker preset EQ: %d", st)
	}
	if s, err := hs.Serial(); err == nil {
		lg.Printf("серийный номер: %s", s)
	}
	if fw, err := hs.DongleFirmware(); err == nil {
		lg.Printf("прошивка донгла: %s", fw)
	}
}

// probe: read-only diagnostics for every collection of the dongle.
func probe(cfg *config.Config, lg *log.Logger) error {
	lg.Printf("===== bsctl %s probe, %s", version.Version, time.Now().Format(time.RFC3339))
	warnSynapse(lg)
	infos, err := hid.Enumerate(headset.VendorRazer, headset.ProductBSV2)
	if err != nil {
		return err
	}
	lg.Printf("HID-коллекций донгла 1532:0555: %d", len(infos))
	for i, info := range infos {
		lg.Printf("  [%d] %s", i, info)
	}
	if len(infos) == 0 {
		return fmt.Errorf("донгл не найден")
	}
	try := func(remote bool) bool {
		for _, info := range infos {
			if info.Err != "" || info.OutLen == 0 || info.InLen == 0 {
				continue
			}
			for _, setOut := range []bool{false, true} {
				method := map[bool]string{false: "WriteFile", true: "HidD_SetOutputReport"}[setOut]
				dev, err := hid.OpenDevice(info)
				if err != nil {
					lg.Printf("--- %s: открыть не удалось: %v", info.Path, err)
					break
				}
				dev.UseSetOutput = setOut
				hs := headset.New(dev, lg, remote)
				hs.Verbose = true
				lg.Printf("--- коллекция %04X:%04X, метод %s, remote=%v", info.UsagePage, info.Usage, method, remote)
				if err = hs.Ping(800*time.Millisecond, 2); err != nil {
					lg.Printf("    нет ответа: %v", err)
					hs.Close()
					continue
				}
				lg.Printf("    ОТВЕЧАЕТ")
				hs.Verbose = false
				printStatus(hs, lg)
				if d, err := hs.AutoPowerOff(); err == nil {
					lg.Printf("auto power off: %v", d)
				}
				hs.Close()
				cfg.DevicePath, cfg.UseSetOutput = info.Path, setOut
				_ = config.Save(cfg)
				return true
			}
		}
		return false
	}
	if try(false) {
		lg.Printf("===== OK: связь есть (без переключения режима)")
		return nil
	}
	lg.Printf("без режима remote ответа нет, пробую включить его (это безопасно, Synapse делает так постоянно)")
	if try(true) {
		lg.Printf("===== OK: связь есть (после включения remote)")
		return nil
	}
	return fmt.Errorf("ни одна коллекция не ответила — пришли bsctl.log")
}

// monitor logs every input report (events from the headset) for diagnostics.
// Every 10 s it also checks whether the dongle answers without remote mode and
// reads the "speaker preset EQ" flag that the tray uses to notice dropped settings.
func monitor(cfg *config.Config, lg *log.Logger, d time.Duration) error {
	hs, err := openHeadsetCLI(cfg, lg)
	if err != nil {
		return err
	}
	defer hs.Close()
	start := time.Now()
	hs.SetReportHook(func(r []byte) {
		var kinds []string
		for _, fr := range headset.ParseFrames(r) {
			kinds = append(kinds, headset.DescribeEvent(fr))
		}
		lg.Printf("[%5.1fs] IN %s  %s", time.Since(start).Seconds(), hid.Hex(hid.TrimZeros(r)), strings.Join(kinds, " | "))
	})
	lg.Printf("===== monitor %v: нажимай кнопку микрофона, крути колесо громкости, включай/выключай видео", d)
	for time.Since(start) < d {
		time.Sleep(10 * time.Second)
		hs.Remote = false
		err := hs.Ping(500*time.Millisecond, 1)
		hs.Remote = cfg.RemoteMode
		st, serr := hs.EQStatus()
		lg.Printf("[%5.1fs] ответ без remote: %v, флаг EQ: %d (%v)", time.Since(start).Seconds(), err == nil, st, serr)
	}
	lg.Printf("===== monitor done")
	return nil
}

// eapoInfo: read-only diagnostics for the Equalizer APO part.
func eapoInfo(cfg *config.Config, lg *log.Logger) error {
	st := eapo.Detect()
	lg.Printf("===== bsctl %s eapo", version.Version)
	lg.Printf("Equalizer APO: установлен=%v, папка %s, конфиг %s", st.Installed, st.InstallDir, st.ConfigDir)
	mics, err := eapo.HeadsetMics()
	if err != nil {
		lg.Printf("звуковые устройства: %v", err)
	}
	for _, m := range mics {
		lg.Printf("микрофон: %s (%s) %s, %s [%s], APO на нём: %v", m.Name, m.Connection, m.GUID, m.Format, m.FormatFrom, m.APOOnIt)
		lg.Printf("  формат в реестре: %s", eapo.HexBytes(m.RegFormat, 48))
	}
	if len(mics) == 0 {
		lg.Printf("микрофон BlackShark среди активных устройств записи не найден")
	}
	for _, f := range []string{"config.txt", eapo.OwnFile} {
		b, err := os.ReadFile(filepath.Join(st.ConfigDir, f))
		if err != nil {
			lg.Printf("%s: %v", f, err)
			continue
		}
		lg.Printf("--- %s:\n%s", f, strings.TrimSpace(string(b)))
	}
	for _, e := range eapo.Engines {
		state := "есть"
		if eapo.FileMissing(eapo.EngineSource(e)) {
			state = "НЕТ"
		}
		lg.Printf("плагин %s: %s (%s)", e.Title, eapo.EngineSource(e), state)
	}
	if n := eapo.VCRTMissing(); n != "" {
		lg.Printf("Visual C++ Redistributable: нет %s (нужен для DeepFilterNet)", n)
	}
	lg.Printf("выбран: %s, включён: %v", eapo.FindEngine(cfg.Noise.Engine).Title, cfg.Noise.Enabled)
	if p := st.ProblemFor(cfg.Noise); p != "" {
		lg.Printf("мешает: %s", p)
	}
	return nil
}

// ---------------- mouse and dock ----------------

func mouseInfo(lg *log.Logger) error {
	lg.Printf("===== bsctl %s mouse, %s", version.Version, time.Now().Format(time.RFC3339))
	warnSynapse(lg)
	found := false
	for _, pid := range []uint16{razer.PIDMouseDock, razer.PIDBasiliskW, razer.PIDBasiliskRx} {
		infos, err := hid.Enumerate(razer.VID, pid)
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			lg.Printf("%s (1532:%04X): нет", razer.PIDName(pid), pid)
			continue
		}
		found = true
		lg.Printf("%s (1532:%04X): %d коллекций", razer.PIDName(pid), pid, len(infos))
		for i, info := range infos {
			lg.Printf("  [%d] %s", i, info)
		}
	}
	if !found {
		return fmt.Errorf("ни док, ни мышь не найдены")
	}
	var dock, mouse *razer.Link
	if l, err := razer.OpenLink(razer.PIDMouseDock, lg, true); err == nil {
		dock = l
		defer l.Close()
		lg.Printf("--- док отвечает: %s", l.Path())
		if fw, err := l.Firmware(true); err == nil {
			lg.Printf("док: прошивка %s", fw)
		}
		l.Verbose = false
		if s, err := l.Serial(true); err == nil {
			lg.Printf("док: серийный номер %s", s)
		} else {
			lg.Printf("док: серийный номер: %v", err)
		}
		if m, err := l.DeviceMode(true); err == nil {
			lg.Printf("док: режим %s", m)
		}
		if b, err := l.DockBrightnessGet(); err == nil {
			lg.Printf("док: яркость %d%%", b)
		} else {
			lg.Printf("док: яркость: %v", err)
		}
	} else if !errors.Is(err, razer.ErrNotFound) {
		lg.Printf("док: %v", err)
	}
	for _, pid := range []uint16{razer.PIDBasiliskW, razer.PIDBasiliskRx} {
		if l, err := razer.OpenLink(pid, lg, true); err == nil {
			mouse = l
			defer l.Close()
			lg.Printf("--- мышь отвечает как %s", razer.PIDName(pid))
			break
		} else if !errors.Is(err, razer.ErrNotFound) {
			lg.Printf("%s: %v", razer.PIDName(pid), err)
		}
	}
	if mouse == nil && dock != nil {
		mouse = dock
		lg.Printf("--- спрашиваю мышь через док")
	}
	if mouse == nil {
		return fmt.Errorf("до мыши не достучаться")
	}
	mouse.Verbose = true
	if fw, err := mouse.Firmware(false); err == nil {
		lg.Printf("мышь: прошивка %s", fw)
	} else {
		lg.Printf("мышь: прошивка: %v", err)
	}
	mouse.Verbose = false
	if s, err := mouse.Serial(false); err == nil {
		lg.Printf("мышь: серийный номер %s", s)
	}
	if lvl, err := mouse.BatteryLevel(); err == nil {
		lg.Printf("мышь: заряд %d%%", lvl)
	} else {
		lg.Printf("мышь: заряд: %v", err)
	}
	if ch, err := mouse.Charging(); err == nil {
		lg.Printf("мышь: заряжается: %v", ch)
	}
	if dock != nil {
		events := make(chan razer.DockEvent, 16)
		stop, n := razer.DockEvents(lg, true, events)
		lg.Printf("слушаю события дока 5 секунд (коллекций открыто: %d)", n)
		deadline := time.After(5 * time.Second)
	loop:
		for {
			select {
			case ev := <-events:
				lg.Printf("событие: %s", ev)
			case <-deadline:
				break loop
			}
		}
		stop()
	}
	lg.Printf("===== mouse done")
	return nil
}

func mouseMonitor(lg *log.Logger, d time.Duration) error {
	events := make(chan razer.DockEvent, 64)
	stop, n := razer.DockEvents(lg, true, events)
	defer stop()
	if n == 0 {
		return fmt.Errorf("не удалось открыть ни одной коллекции дока для чтения событий")
	}
	lg.Printf("===== mouse-monitor %v (%d коллекций): поставь мышь на док, сними, выключи/включи её", d, n)
	deadline := time.After(d)
	start := time.Now()
	for {
		select {
		case ev := <-events:
			lg.Printf("[%5.1fs] %s", time.Since(start).Seconds(), ev)
		case <-deadline:
			lg.Printf("===== mouse-monitor done")
			return nil
		}
	}
}

func dockCmd(args []string, cfg *config.Config, lg *log.Logger) error {
	if len(args) == 0 {
		return fmt.Errorf("bsctl dock test | battery | RRGGBB | breath RRGGBB | spectrum | off | bright <0-100> | mode 0|3")
	}
	warnSynapse(lg)
	dock, err := razer.OpenLink(razer.PIDMouseDock, lg, false)
	if err != nil {
		return fmt.Errorf("док: %v", err)
	}
	defer dock.Close()
	set := func(effect byte, colors ...[3]byte) error {
		var c [3]byte
		if len(colors) > 0 {
			c = colors[0]
		}
		if err := dock.DockEffect(effect, colors...); err != nil {
			return err
		}
		lg.Printf("док -> %s", razer.FxName(effect, c))
		return nil
	}
	switch strings.ToLower(args[0]) {
	case "test":
		if err := dock.DockBrightness(max(cfg.Mouse.DockBrightness, 30)); err != nil {
			return err
		}
		steps := []struct {
			fx  byte
			col [3]byte
		}{{razer.FxStatic, razer.ColGreen}, {razer.FxStatic, razer.ColYellow}, {razer.FxStatic, razer.ColOrange}, {razer.FxStatic, razer.ColRed}, {razer.FxBreathing, razer.ColGreen}, {razer.FxSpectrum, [3]byte{}}}
		for _, s := range steps {
			var err error
			if s.fx == razer.FxSpectrum {
				err = set(s.fx)
			} else {
				err = set(s.fx, s.col)
			}
			if err != nil {
				return err
			}
			time.Sleep(3 * time.Second)
		}
		lg.Printf("тест закончен. Если цвета менялись — подсветка дока работает. Трей сам покрасит док по заряду.")
		return nil
	case "battery":
		lvl, err := dock.BatteryLevel()
		if err != nil {
			lg.Printf("мышь не ответила (%v) — как Synapse, крашу в красный", err)
			return set(razer.DockLook(-1, false))
		}
		ch, _ := dock.Charging()
		lg.Printf("мышь: %d%%, заряжается: %v", lvl, ch)
		_ = dock.DockBrightness(cfg.Mouse.DockBrightness)
		return set(razer.DockLook(lvl, ch))
	case "spectrum":
		return set(razer.FxSpectrum)
	case "off":
		return set(razer.FxOff)
	case "breath":
		if len(args) < 2 {
			return fmt.Errorf("bsctl dock breath RRGGBB")
		}
		c, err := razer.ParseColor(args[1])
		if err != nil {
			return err
		}
		return set(razer.FxBreathing, c)
	case "mode":
		var n int
		if len(args) < 2 {
			return fmt.Errorf("bsctl dock mode 0|3")
		}
		if _, err := fmt.Sscan(args[1], &n); err != nil || (n != 0 && n != 3) {
			return fmt.Errorf("режим 0 (обычный) или 3 (как при Synapse)")
		}
		if err := dock.SetDeviceMode(true, byte(n)); err != nil {
			return err
		}
		m, _ := dock.DeviceMode(true)
		lg.Printf("режим дока: %s", m)
		return nil
	case "bright":
		var n int
		if len(args) < 2 {
			return fmt.Errorf("bsctl dock bright <0-100>")
		}
		if _, err := fmt.Sscan(args[1], &n); err != nil || n < 0 || n > 100 {
			return fmt.Errorf("яркость 0..100")
		}
		if err := dock.DockBrightness(n); err != nil {
			return err
		}
		cfg.Mouse.DockBrightness = n
		lg.Printf("яркость дока: %d%%", n)
		return config.Save(cfg)
	default:
		c, err := razer.ParseColor(args[0])
		if err != nil {
			return err
		}
		return set(razer.FxStatic, c)
	}
}
