package voicemix

import (
	"errors"
	"testing"

	"github.com/wasmdesk/wasmbox/audioring"

	"github.com/go-quake1/engine/sound"
)

// fakeHost applies commands the way the worklet does (audioring.DecodeCommand
// for validation, the generation rule, a frames-left counter per slot) so a test
// can play sounds to their end.
type fakeHost struct {
	loads   map[int]int // id -> frames
	loadErr error
	commits int
	sizes   []int // commands per commit
	gen     [audioring.VoiceSlots]uint16
	active  [audioring.VoiceSlots]bool
	left    [audioring.VoiceSlots]int
	sample  [audioring.VoiceSlots]int
	started [audioring.VoiceSlots]int // START commands applied, per slot
	bad     int
	commitE error
}

func newFake() *fakeHost { return &fakeHost{loads: map[int]int{}} }

func (f *fakeHost) Load(id, bits, rate, loopStart, frames int, pcm []byte) error {
	if f.loadErr != nil {
		return f.loadErr
	}
	if _, ok := f.loads[id]; ok {
		return errors.New("reload")
	}
	if rate != audioring.MixRate || len(pcm) != frames*bits/8 || loopStart < 0 || loopStart >= frames {
		return errors.New("bad load")
	}
	f.loads[id] = frames
	return nil
}

func (f *fakeHost) Commit(words []int32, snap *[2 * audioring.VoiceSlots]uint32) error {
	if f.commitE != nil {
		return f.commitE
	}
	f.commits++
	f.sizes = append(f.sizes, len(words)/4)
	for i := 0; i+4 <= len(words); i += 4 {
		c, err := audioring.DecodeCommand([4]int32{words[i], words[i+1], words[i+2], words[i+3]})
		if err != nil {
			f.bad++
			continue
		}
		switch {
		case c.Op == audioring.OpStart && (c.Gen == f.gen[c.Slot] || c.Gen == f.gen[c.Slot]+1):
			if _, ok := f.loads[c.Sample]; !ok {
				f.bad++
				continue
			}
			f.gen[c.Slot], f.active[c.Slot], f.left[c.Slot], f.sample[c.Slot] = c.Gen, true, f.loads[c.Sample], c.Sample
			f.started[c.Slot]++
		case c.Op != audioring.OpStart && c.Gen == f.gen[c.Slot] && f.active[c.Slot]:
			if c.Op == audioring.OpStop {
				f.active[c.Slot] = false
			}
		default:
			f.bad++
		}
	}
	for s := range f.gen {
		w := uint32(f.gen[s])
		if f.active[s] {
			w |= 1 << 16
		}
		snap[2*s], snap[2*s+1] = w, uint32(f.left[s])
	}
	return nil
}

// play advances the fake worklet by n frames.
func (f *fakeHost) play(n int) {
	for s := range f.active {
		if f.active[s] {
			f.left[s] -= n
			if f.left[s] <= 0 {
				f.left[s], f.active[s] = 0, false
			}
		}
	}
}

func sample(name string, n int) *sound.Sample {
	return &sound.Sample{Name: name, SampleRate: 11025, BitsPerSam: 8, LoopStart: -1, NumSamples: n, Data: make([]byte, n)}
}

func pool(t *testing.T, m *Mixer) *sound.Pool {
	t.Helper()
	p, err := sound.NewPool(8)
	if err != nil {
		t.Fatal(err)
	}
	p.AttachVoices(m)
	return p
}

// play puts s on a fresh channel of p the way host.StartSound does.
func startOn(p *sound.Pool, s *sound.Sample, ent int) int {
	slot, err := p.Alloc(ent, 1)
	if err != nil {
		panic(err)
	}
	c := &p.Channels[slot]
	c.Sfx, c.Position, c.EndPos, c.Looping, c.LeftVol, c.RightVol, c.EntNum, c.EntChannel = s, 0, s.NumSamples, false, 200, 100, ent, 1
	p.Launch(slot)
	return slot
}

func TestASoundLoadsOnceAndPlaysToItsEnd(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	s := sample("a.wav", 1000)
	a := startOn(p, s, 1)
	b := startOn(p, s, 2)
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(f.loads) != 1 || f.loads[0] != 1000 {
		t.Fatalf("loads = %v, want the sample once", f.loads)
	}
	if f.started[a] != 1 || f.started[b] != 1 || f.bad != 0 {
		t.Fatalf("started %d %d, bad %d", f.started[a], f.started[b], f.bad)
	}
	// Not finished: the channels keep their slots and learn what they have left.
	p.Sync()
	if p.Channels[a].Free() || p.Channels[a].Position != 0 {
		t.Fatalf("channel %d = %+v right after the start", a, p.Channels[a])
	}
	f.play(400)
	m.Commit()
	p.Sync()
	if p.Channels[a].Free() || p.Channels[a].Position != 400 {
		t.Fatalf("after 400 frames: free=%v position=%d, want playing at 400", p.Channels[a].Free(), p.Channels[a].Position)
	}
	f.play(700)
	m.Commit()
	p.Sync()
	if !p.Channels[a].Free() || !p.Channels[b].Free() {
		t.Fatal("the finished sounds still hold their channels")
	}
	if got, _ := p.Alloc(9, 1); got != a && got != b && got != 8 {
		t.Fatalf("Alloc = %d, want a freed slot", got)
	}
}

func TestAStartNotYetCommittedIsNotRetired(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	a := startOn(p, sample("a", 100), 1)
	m.Commit() // snapshot: a plays
	f.play(1000)
	m.Commit() // a ended
	if !m.Retired(a) {
		t.Fatal("a finished sound is not retired")
	}
	// Restarted but not committed: the snapshot is of the old sound.
	p.Channels[a].Stop()
	b := startOn(p, sample("b", 100), 1)
	if b != a {
		t.Skipf("slot reuse expected, got %d then %d", a, b)
	}
	if m.Retired(b) {
		t.Fatal("a start still in the queue reads as retired from the old sound's snapshot")
	}
}

func TestRetiredNeedsASlotTheMixerWasToldAbout(t *testing.T) {
	m := New(newFake())
	m.Commit()
	if m.Retired(5) || m.Remaining(5) != -1 {
		t.Fatal("a slot nothing started is retired or has a position")
	}
}

func TestACommitEveryTicRefreshesTheSnapshotEvenWithNothingToSay(t *testing.T) {
	f := newFake()
	m := New(f)
	m.Commit()
	m.Commit()
	if f.commits != 2 || f.sizes[0] != 0 || f.sizes[1] != 0 {
		t.Fatalf("commits %d sizes %v, want two empty calls", f.commits, f.sizes)
	}
}

func TestMoreCommandsThanTheRingHoldsAreSplit(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	s := sample("a", 5)
	slot := startOn(p, s, 1)
	m.Commit()
	for i := 0; i < 2*audioring.CmdEntries+7; i++ {
		m.SetVolume(slot, i%256, 1)
	}
	if err := m.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := f.sizes[len(f.sizes)-3:]; got[0] != 1024 || got[1] != 1024 || got[2] != 7 {
		t.Fatalf("batches %v, want 1024 1024 7", got)
	}
}

func TestARefusedLoadPlaysNothingAndIsCounted(t *testing.T) {
	f := newFake()
	f.loadErr = errors.New("bank full")
	m := New(f)
	p := pool(t, m)
	startOn(p, sample("big", 10), 1)
	m.Commit()
	if m.Refused != 1 || m.LastErr == nil {
		t.Fatalf("Refused = %d, want 1", m.Refused)
	}
	for s, n := range f.started {
		if n != 0 {
			t.Fatalf("slot %d started a sound that never loaded", s)
		}
	}
}

func TestACommitErrorDropsTheBatchAndCountsIt(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	startOn(p, sample("a", 10), 1)
	f.commitE = errors.New("ring full")
	if err := m.Commit(); err == nil || m.Refused != 1 {
		t.Fatalf("err %v refused %d", err, m.Refused)
	}
	f.commitE = nil
	m.Commit()
	if f.commits != 1 || f.sizes[0] != 0 {
		t.Fatalf("the dropped batch came back: %v", f.sizes)
	}
}

func TestTheGameNeverGetsTheMusicSlots(t *testing.T) {
	m := New(newFake())
	p := pool(t, m)
	seen := map[int]bool{}
	for i := 0; i < 3*sound.MaxChannels; i++ {
		slot, err := p.Alloc(100+i, 1)
		if err != nil {
			t.Fatal(err)
		}
		if slot >= sound.MaxChannels-sound.MusicSlots {
			t.Fatalf("Alloc returned music slot %d", slot)
		}
		seen[slot] = true
		c := &p.Channels[slot]
		c.Sfx, c.EndPos, c.EntNum, c.EntChannel = sample("x", 100), 100, 100+i, 1
	}
	if len(seen) != sound.MaxChannels-sound.MusicSlots-8 {
		t.Fatalf("used %d dynamic slots, want %d", len(seen), sound.MaxChannels-sound.MusicSlots-8)
	}
}

func TestMusicPlaysOnItsTwoSlotsAndSurvivesStopAll(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	l, r := make([]int16, 5000), make([]int16, 5000)
	if err := m.SetMusic(l, r, 100); err != nil {
		t.Fatal(err)
	}
	startOn(p, sample("a", 10), 1)
	m.Commit()
	p.StopAll()
	m.Commit()
	left, right := audioring.VoiceSlots-2, audioring.VoiceSlots-1
	if !f.active[left] || !f.active[right] {
		t.Fatal("StopAll stopped the music")
	}
	if f.sample[left] != musicBase || f.sample[right] != musicBase+1 {
		t.Fatalf("music samples %d %d", f.sample[left], f.sample[right])
	}
	if err := m.SetMusic(nil, nil, -1); err != nil {
		t.Fatal(err)
	}
	m.Commit()
	if f.active[left] || f.active[right] || f.bad != 0 {
		t.Fatalf("the music did not stop cleanly: active %v %v, bad %d", f.active[left], f.active[right], f.bad)
	}
}

func TestEachTrackTakesNewBankIdsUntilTheyRunOut(t *testing.T) {
	m := New(newFake())
	pcm := make([]int16, 4)
	n := 0
	for m.SetMusic(pcm, pcm, -1) == nil {
		n++
		if n > 100 {
			t.Fatal("no limit")
		}
	}
	if n != (audioring.MaxSamples-musicBase)/2 {
		t.Fatalf("%d tracks before the ids ran out, want %d", n, (audioring.MaxSamples-musicBase)/2)
	}
}

func TestAnInvalidCommandIsRefusedNotQueued(t *testing.T) {
	m := New(newFake())
	m.push(audioring.Command{Op: 99})
	if m.Refused != 1 || len(m.words) != 0 || m.LastErr == nil {
		t.Fatalf("refused %d words %d", m.Refused, len(m.words))
	}
}

func TestVolumesAreClampedToTheRange(t *testing.T) {
	f := newFake()
	m := New(f)
	p := pool(t, m)
	slot := startOn(p, sample("a", 10), 1)
	p.Channels[slot].LeftVol, p.Channels[slot].RightVol = -5, 999
	p.Retune(slot)
	m.Start(slot, sample("b", 10), false, -1, audioring.MaxVolume+1)
	if m.Refused != 0 {
		t.Fatalf("an out-of-range volume was refused (%v) instead of clamped", m.LastErr)
	}
	if got := len(m.words) / 4; got != 3 {
		t.Fatalf("%d commands queued, want start, setvol, start", got)
	}
	c, _ := audioring.DecodeCommand([4]int32{m.words[4], m.words[5], m.words[6], m.words[7]})
	if c.Left != 0 || c.Right != audioring.MaxVolume {
		t.Fatalf("clamped to (%d,%d)", c.Left, c.Right)
	}
}

func TestSlotsOutsideTheGamesRangeAreIgnored(t *testing.T) {
	f := newFake()
	m := New(f)
	if err := m.SetMusic(make([]int16, 10), make([]int16, 10), -1); err != nil {
		t.Fatal(err)
	}
	m.Commit()
	base := len(f.loads)
	for _, slot := range []int{audioring.VoiceSlots - 2, audioring.VoiceSlots - 1} {
		m.Start(slot, sample("a", 10), false, 100, 100)
		m.SetVolume(slot, 1, 1)
		m.Stop(slot)
		if m.Remaining(slot) != -1 || m.Retired(slot) {
			t.Fatalf("slot %d answers", slot)
		}
	}
	if len(m.words) != 0 || len(f.loads) != base {
		t.Fatalf("%d commands, %d loads for slots that are not the game's", len(m.words)/4, len(f.loads)-base)
	}
}

func TestVolumeAndStopNeedALiveSlot(t *testing.T) {
	m := New(newFake())
	m.SetVolume(3, 1, 1)
	m.Stop(3)
	if len(m.words) != 0 {
		t.Fatalf("%d commands for a slot nothing started", len(m.words)/4)
	}
}

func TestARefusedMusicLoadStartsNothing(t *testing.T) {
	f := newFake()
	f.loadErr = errors.New("bank full")
	m := New(f)
	if err := m.SetMusic(make([]int16, 10), make([]int16, 10), -1); err == nil || m.Refused != 1 {
		t.Fatalf("err %v refused %d", err, m.Refused)
	}
	if len(m.words) != 0 || m.musicOn {
		t.Fatal("music started though its samples did not load")
	}
}

// noPanic fails t when f panics: a slot outside the table must be ignored, and
// an index panic would be that guard missing.
func noPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

func TestNegativeAndHugeSlotsDoNotIndexTheTables(t *testing.T) {
	m := New(newFake())
	m.Commit()
	for _, slot := range []int{-1, -1000, audioring.VoiceSlots, 1 << 20} {
		noPanic(t, "Start", func() { m.Start(slot, sample("a", 4), false, 1, 1) })
		noPanic(t, "SetVolume", func() { m.SetVolume(slot, 1, 1) })
		noPanic(t, "Stop", func() { m.Stop(slot) })
		noPanic(t, "Retired", func() { _ = m.Retired(slot) })
		noPanic(t, "Remaining", func() {
			if got := m.Remaining(slot); got != -1 {
				t.Errorf("Remaining(%d) = %d", slot, got)
			}
		})
	}
}
