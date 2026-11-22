package leveldb

// AddReplayJournal adds to dir the replay journal OpenWritable would add
// before opening it, and nothing else, so tests can check how both
// libraries read the state a crash right after that step leaves. It
// returns the journal's name, or "" when dir needs none (no journal is
// behind the manifest, or the newest journal already replays them).
func AddReplayJournal(dir string) (string, error) {
	st, err := inspectJournals(dir, writableOptions())
	if err != nil || st.behind == "" || st.replayed() {
		return "", err
	}
	return writeReplayJournal(dir, st)
}
