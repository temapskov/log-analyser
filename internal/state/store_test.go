package state

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Config{Path: filepath.Join(dir, "state.db")}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpen_MemoryAndMigrate(t *testing.T) {
	s, err := Open(Config{Path: ":memory:"}, nil)
	if err != nil {
		t.Fatalf("Open :memory:: %v", err)
	}
	defer s.Close()
	// Sanity: table runs существует и пуста.
	runs, err := s.RecentRuns(context.Background(), 5)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("только что созданная БД не пуста: %d", len(runs))
	}
}

func TestOpen_RejectsEmptyPath(t *testing.T) {
	if _, err := Open(Config{}, nil); err == nil {
		t.Fatal("ожидали ошибку на пустой Path")
	}
}

func TestBeginRun_FreshInsert(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	from, to := time.Unix(1000, 0), time.Unix(2000, 0)

	id, resumed, err := s.BeginRun(ctx, from, to)
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if resumed {
		t.Error("fresh insert не должен быть resumed")
	}
	if len(id) != 32 {
		t.Errorf("id length: %d (%q)", len(id), id)
	}

	// Повторно тот же run — resumed, status 'started'.
	id2, resumed, err := s.BeginRun(ctx, from, to)
	if err != nil {
		t.Fatalf("BeginRun 2: %v", err)
	}
	if !resumed {
		t.Error("ожидали resumed=true для того же окна")
	}
	if id2 != id {
		t.Errorf("id должен совпасть: %q vs %q", id2, id)
	}
}

func TestBeginRun_WindowValidation(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	_, _, err := s.BeginRun(ctx, time.Unix(2000, 0), time.Unix(1000, 0))
	if err == nil {
		t.Fatal("ожидали ошибку обратного окна")
	}
}

func TestFinishAndIdempotencyCheck(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	from, to := time.Unix(3000, 0), time.Unix(4000, 0)

	id, _, err := s.BeginRun(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCoverSent(ctx, id, 100500); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishRun(ctx, id, 5); err != nil {
		t.Fatal(err)
	}

	// FindCompletedRun возвращает запись.
	run, err := s.FindCompletedRun(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("ожидали run")
	}
	if run.Status != StatusDone {
		t.Errorf("status: %q", run.Status)
	}
	if run.FilesSent != 5 {
		t.Errorf("files_sent: %d", run.FilesSent)
	}
	if !run.CoverMsgID.Valid || run.CoverMsgID.Int64 != 100500 {
		t.Errorf("cover id: %+v", run.CoverMsgID)
	}

	// BeginRun на то же окно возвращает ErrAlreadyDelivered.
	_, resumed, err := s.BeginRun(ctx, from, to)
	if !errors.Is(err, ErrAlreadyDelivered) {
		t.Errorf("ожидали ErrAlreadyDelivered, got %v", err)
	}
	if !resumed {
		t.Error("resumed должен быть true")
	}
}

func TestFailRun(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	from, to := time.Unix(5000, 0), time.Unix(6000, 0)
	id, _, err := s.BeginRun(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailRun(ctx, id, "boom"); err != nil {
		t.Fatal(err)
	}
	// FindCompletedRun теперь nil — status не done.
	if run, err := s.FindCompletedRun(ctx, from, to); err != nil || run != nil {
		t.Errorf("должен быть nil: run=%v err=%v", run, err)
	}
	// Но BeginRun возвращает existing id, resumed=true.
	id2, resumed, err := s.BeginRun(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id || !resumed {
		t.Errorf("failed run должен быть resumeable: id=%s resumed=%v", id2, resumed)
	}
}

func TestFindCompletedRun_NoMatch(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	run, err := s.FindCompletedRun(ctx, time.Unix(0, 0), time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if run != nil {
		t.Errorf("ожидали nil: %+v", run)
	}
}

func TestRecentRuns_OrderedDesc(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	// Три окна с разрывом в минуту — будут три записи.
	for i := 0; i < 3; i++ {
		from := time.Unix(int64(i*1000), 0)
		to := from.Add(time.Minute)
		id, _, err := s.BeginRun(ctx, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.FinishRun(ctx, id, 1); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // разнесли created_at
	}
	runs, err := s.RecentRuns(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 {
		t.Fatalf("ожидали 3, got %d", len(runs))
	}
	// Новые первыми.
	for i := 1; i < len(runs); i++ {
		if runs[i-1].CreatedAt.Before(runs[i].CreatedAt) {
			t.Errorf("порядок нарушен: %v %v", runs[i-1].CreatedAt, runs[i].CreatedAt)
		}
	}
}

// TestBeginRun_ConcurrentSameWindow — две горутины пытаются BeginRun одно
// и то же окно. Должен получиться 1 run, второй — resumed.
func TestBeginRun_ConcurrentSameWindow(t *testing.T) {
	s := openTempStore(t)
	ctx := context.Background()
	from, to := time.Unix(7000, 0), time.Unix(8000, 0)

	var wg sync.WaitGroup
	ids := make([]string, 2)
	resumedFlags := make([]bool, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ids[i], resumedFlags[i], errs[i] = s.BeginRun(ctx, from, to)
		}()
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if errs[i] != nil {
			t.Fatalf("BeginRun #%d: %v", i, errs[i])
		}
	}
	if ids[0] != ids[1] {
		t.Errorf("id должны совпасть: %q vs %q", ids[0], ids[1])
	}
	// Ровно один — freshly inserted.
	fresh := 0
	for _, r := range resumedFlags {
		if !r {
			fresh++
		}
	}
	if fresh != 1 {
		t.Errorf("ровно 1 goroutine должен видеть fresh insert, got %d", fresh)
	}
}

func TestStore_Persists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	// Первый open: insert + finish.
	s1, err := Open(Config{Path: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	from, to := time.Unix(9000, 0), time.Unix(10000, 0)
	id, _, err := s1.BeginRun(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.FinishRun(context.Background(), id, 3); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	// Второй open (тот же файл): запись видна.
	s2, err := Open(Config{Path: path}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	run, err := s2.FindCompletedRun(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("persisted run не найден после переоткрытия")
	}
	if run.ID != id {
		t.Errorf("id: %q vs %q", run.ID, id)
	}
}
