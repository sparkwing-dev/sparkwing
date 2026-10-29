package acceptance_test

import "testing"

func TestEnvironment(t *testing.T) {
	runCell(t, "capturedEnvironment", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := capturedEnvironment(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "remoteEnvironment", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := remoteEnvironment(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "environmentPhases", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := environmentPhases(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "boundedEnvironmentText", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := boundedEnvironmentText(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "environmentDeadlines", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := environmentDeadlines(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "settingSeals", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := settingSeals(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "independentRunBounds", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := independentRunBounds(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "authorBounds", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := authorBounds(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "retrySeals", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := retrySeals(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "repositoryKeys", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := repositoryKeys(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "classifiedDisplay", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := classifiedDisplay(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "defaultBranchSeals", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := defaultBranchSeals(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "independentJobTimeouts", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := independentJobTimeouts(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "configDecoderBoundaries", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := configDecoderBoundaries(); err != nil {
			t.Fatal(err)
		}
	})
	runCell(t, "configFinalSchema", "in-process", func(t *testing.T) {
		recordLeaf(t)
		if err := configFinalSchema(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStorageBoundaries(t *testing.T) {
	runCell(t, "checkStorageCAS", "in-process", checkStorageCAS)
	runCell(t, "checkStorageReceipts", "in-process", checkStorageReceipts)
	runCell(t, "checkStorageNamespaces", "in-process", checkStorageNamespaces)
	runCell(t, "checkStorageOwnerReceipts", "in-process", checkStorageOwnerReceipts)
	runCell(t, "checkStoragePresence", "in-process", checkStoragePresence)
	runCell(t, "checkStorageRemoteCache", "in-process", checkStorageRemoteCache)
}
