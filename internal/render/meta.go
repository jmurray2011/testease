package render

import (
	"fmt"
	"io"

	"github.com/jmurray2011/testease/internal/record"
)

// Meta regenerates run.json from the events, at any time.
//
// It is a rendering like every other view here: the summary is a cache of the
// record, so producing one on demand and producing one at the end of a run are
// the same operation, and neither is the evidence.
func Meta(data, notices io.Writer, rec *record.Record) error {
	if err := writeNotices(notices, rec); err != nil {
		return err
	}
	body, err := record.EncodeSummary(record.Summarize(rec))
	if err != nil {
		return err
	}
	if _, err := data.Write(body); err != nil {
		return fmt.Errorf("render meta: %w", err)
	}
	return nil
}
