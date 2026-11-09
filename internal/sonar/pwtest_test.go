package sonar

import (
	"encoding/binary"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pwTest is Sonar running on a private PipeWire instance with WirePlumber
// (see TestRoutingOnPipeWire), with the instance's test_headset_mic as the
// microphone and a test_head output.
type pwTest struct {
	t   *testing.T
	s   *Service
	srv *httptest.Server
	tmp string
}

func newPWTest(t *testing.T) *pwTest {
	runtime, data := os.Getenv("GG_TEST_PIPEWIRE"), os.Getenv("GG_LINUX_DATA")
	if runtime == "" || data == "" {
		t.Skip("GG_TEST_PIPEWIRE or GG_LINUX_DATA not set")
	}
	tmp := t.TempDir()
	os.MkdirAll(filepath.Join(tmp, "sonar"), 0o755)
	os.MkdirAll(filepath.Join(tmp, "state"), 0o755)
	if err := exec.Command("cp", "-r", filepath.Join(data, "sonar", "db-migrations"), filepath.Join(tmp, "sonar")).Run(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GG_LINUX_DATA", tmp)
	t.Setenv("PIPEWIRE_RUNTIME_DIR", runtime)

	out, err := pwCommand("pw-cli", "create-node", "adapter",
		`{ factory.name = support.null-audio-sink node.name = test_head media.class = Audio/Sink audio.position = [ FL FR ] object.linger = true }`).CombinedOutput()
	if err != nil {
		t.Fatalf("pw-cli: %v: %s", err, out)
	}
	t.Cleanup(func() { pwCommand("pw-cli", "destroy", "test_head").Run() })
	time.Sleep(500 * time.Millisecond)

	s, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	s.SetActive(true)
	t.Cleanup(func() { s.SetActive(false) })
	p := &pwTest{t: t, s: s, srv: httptest.NewServer(s.Handler()), tmp: tmp}
	t.Cleanup(p.srv.Close)
	// Fallback would take the virtual test microphone, which Sonar does not
	// list, for a missing device.
	p.call("PUT", "/fallbackSettings/isEnabled/false", "")
	p.call("POST", "/onboarding", `{"devices":{"game":"test_head","chatRender":"test_head","chatCapture":"test_headset_mic"}}`)
	return p
}

// call makes an API request and returns the response body, failing the test
// on an error status.
func (p *pwTest) call(method, path, body string) string {
	p.t.Helper()
	req, _ := http.NewRequest(method, p.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		p.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return string(b)
}

// level plays a recording into a node (a sink, or the test microphone)
// and returns the level device records, in dBFS: a sink's monitor or a
// source.
func (p *pwTest) level(device, into, wav string) float64 {
	t := p.t
	t.Helper()
	time.Sleep(1500 * time.Millisecond) // let relinking settle
	// pw-record falls back to the default device for a missing one.
	objs, err := pwDump()
	if err != nil {
		t.Fatal(err)
	}
	props := ""
	for _, o := range objs {
		if name, _ := o.Info.Props["node.name"].(string); name == device && o.Type == "PipeWire:Interface:Node" {
			class, _ := o.Info.Props["media.class"].(string)
			props = "{ }"
			if strings.HasPrefix(class, "Audio/Sink") {
				props = "{ stream.capture.sink = true }"
			}
		}
	}
	if props == "" {
		t.Fatalf("no device %s", device)
	}
	rec := filepath.Join(p.tmp, "rec.wav")
	os.Remove(rec)
	r := pwCommand("pw-record", "--target", device, "-P", props, "--rate", "48000", "--channels", "2", "--format", "s16", rec)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if into == "test_headset_mic" {
		pl := pwCommand("pw-cat", "-p", "-P", "{ node.name = mic_tone node.autoconnect = false }", wav)
		pl.Start()
		defer pl.Process.Kill()
		time.Sleep(500 * time.Millisecond)
		for _, ch := range []string{"FL", "FR"} {
			if out, err := pwCommand("pw-link", "mic_tone:output_"+ch, "test_headset_mic:input_MONO").CombinedOutput(); err != nil {
				t.Fatalf("linking the microphone tone: %v: %s", err, out)
			}
		}
		pl.Wait()
	} else {
		pwCommand("pw-cat", "-p", "--target", into, wav).Run()
	}
	r.Process.Signal(syscall.SIGINT)
	r.Wait()
	return rmsDB(t, rec)
}

func (p *pwTest) near(what string, got, want float64) {
	p.t.Helper()
	if math.Abs(got-want) > 1 {
		p.t.Errorf("%s: %.1f dBFS, want %.1f", what, got, want)
	} else {
		p.t.Logf("%s: %.1f dBFS", what, got)
	}
}

func (p *pwTest) silent(what string, got float64) {
	p.t.Helper()
	if got > -70 {
		p.t.Errorf("%s: %.1f dBFS, want silence", what, got)
	} else {
		p.t.Logf("%s: silent", what)
	}
}

func rmsDB(t *testing.T, path string) float64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || len(b) < 44 {
		t.Fatalf("recording %s: %v", path, err)
	}
	pcm := b[44:]
	n := len(pcm) / 2
	var sum float64
	from, to := n/4, n*3/4
	for i := from; i < to; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sum += v * v
	}
	r := math.Sqrt(sum / float64(to-from))
	if r == 0 {
		return -120
	}
	return 20 * math.Log10(r)
}

// writeWAV writes 48 kHz stereo 16-bit audio, the same in both channels.
func writeWAV(t *testing.T, path string, samples []float64) {
	pcm := make([]byte, 0, len(samples)*4)
	for _, s := range samples {
		v := int16(math.Max(-32768, math.Min(32767, s*32768)))
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
		pcm = binary.LittleEndian.AppendUint16(pcm, uint16(v))
	}
	h := []byte("RIFF")
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(pcm)))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 1)
	h = binary.LittleEndian.AppendUint16(h, 2)
	h = binary.LittleEndian.AppendUint32(h, 48000)
	h = binary.LittleEndian.AppendUint32(h, 48000*4)
	h = binary.LittleEndian.AppendUint16(h, 4)
	h = binary.LittleEndian.AppendUint16(h, 16)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(pcm)))
	if err := os.WriteFile(path, append(h, pcm...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTone writes a 440 Hz tone at -30 dBFS peak.
func writeTone(t *testing.T, path string, d time.Duration) {
	s := make([]float64, int(d.Seconds()*48000))
	for i := range s {
		s[i] = 1000.0 / 32768 * math.Sin(2*math.Pi*440*float64(i)/48000)
	}
	writeWAV(t, path, s)
}

// writeNoise writes white noise at an RMS level in dBFS, with a tone at
// -30 dBFS peak over it if tone is set.
func writeNoise(t *testing.T, path string, d time.Duration, rmsDB float64, tone bool) {
	r := rand.New(rand.NewSource(1))
	amp := math.Pow(10, rmsDB/20)
	s := make([]float64, int(d.Seconds()*48000))
	for i := range s {
		s[i] = amp * r.NormFloat64()
		if tone {
			s[i] += 1000.0 / 32768 * math.Sin(2*math.Pi*440*float64(i)/48000)
		}
	}
	writeWAV(t, path, s)
}
