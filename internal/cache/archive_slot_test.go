package cache

import (
 "errors"
 "path/filepath"
 "testing"
 "time"
)

func TestArchiveCreationRefusesBeforeStartingGitWhenSlotsAreFull(t *testing.T) {
 t.Setenv("PATH",t.TempDir())
 release:=holdGitForkSlot(t);defer release()
 shortenGitForkWait(t,20*time.Millisecond)
 err:=archiveToFile(t.TempDir(),"main",filepath.Join(t.TempDir(),"archive.tar.gz"))
 if !errors.Is(err,errGitForkUnavailable){t.Fatalf("archive reached git instead of refusing at the fork limit: %v",err)}
}
