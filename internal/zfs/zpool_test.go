package zfs

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func stubZpool(t *testing.T, stdout, stderr string, err error) *[]string {
	t.Helper()

	var got []string
	prev := execZpool
	execZpool = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		got = args
		return []byte(stdout), []byte(stderr), err
	}
	t.Cleanup(func() { execZpool = prev })
	return &got
}

func TestSpaceParsesZpoolList(t *testing.T) {
	// Real output of: zpool list -Hp -o size,allocated,free zdap
	args := stubZpool(t, "10200547328\t103773184\t10096774144\n", "", nil)

	space, err := NewZFS("zdap/1/databases").Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := PoolSpace{Total: 10200547328, Used: 103773184, Free: 10096774144}
	if space != want {
		t.Errorf("space = %+v, want %+v", space, want)
	}
	if space.Used+space.Free != space.Total {
		t.Errorf("used + free = %d, want total %d", space.Used+space.Free, space.Total)
	}

	// The pool is asked about, not the dataset, and in exact bytes.
	wantArgs := []string{"list", "-Hp", "-o", "size,allocated,free", "zdap"}
	if !reflect.DeepEqual(*args, wantArgs) {
		t.Errorf("args = %q, want %q", *args, wantArgs)
	}
}

func TestSpaceRejectsUnexpectedOutput(t *testing.T) {
	for name, out := range map[string]string{
		"empty":      "",
		"too few":    "10200547328\t103773184\n",
		"too many":   "1\t2\t3\t4\n",
		"not a size": "10200547328\tlots\t10096774144\n",
		"human read": "9.50G\t116M\t9.39G\n",
	} {
		t.Run(name, func(t *testing.T) {
			stubZpool(t, out, "", nil)
			if _, err := NewZFS("zdap/1/databases").Space(context.Background()); err == nil {
				t.Errorf("want an error for %q", out)
			}
		})
	}
}

func TestSpaceUsesOnlyTheFirstLine(t *testing.T) {
	stubZpool(t, "1\t2\t3\n4\t5\t6\n", "", nil)

	space, err := NewZFS("zdap/1/databases").Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (PoolSpace{Total: 1, Used: 2, Free: 3}); space != want {
		t.Errorf("space = %+v, want %+v", space, want)
	}
}

// TestSpaceLive runs the real zpool command. Reading pool capacity does not
// need root, so unlike the channel program paths this can be checked here.
func TestSpaceLive(t *testing.T) {
	pool := os.Getenv("ZDAP_BENCH_ZPOOL")
	if pool == "" {
		t.Skip("set ZDAP_BENCH_ZPOOL to a zdap pool, e.g. zdap/1/databases, to run this test")
	}

	space, err := NewZFS(pool).Space(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if space.Total == 0 {
		t.Error("total = 0, want the pool's size")
	}
	if space.Used+space.Free != space.Total {
		t.Errorf("used %d + free %d != total %d", space.Used, space.Free, space.Total)
	}
	t.Logf("pool %s: %d total, %d used, %d free", poolName(pool), space.Total, space.Used, space.Free)
}

func TestSpaceReportsCommandFailure(t *testing.T) {
	stubZpool(t, "", "cannot open 'nope': no such pool\n", errors.New("exit status 1"))

	_, err := NewZFS("nope/1/databases").Space(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "no such pool") {
		t.Errorf("error = %v, want it to carry the stderr message", err)
	}
}
