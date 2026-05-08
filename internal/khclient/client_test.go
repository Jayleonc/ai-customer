package khclient

import "testing"

func TestRetrieveResponseNormalizesResultsFromEvidence(t *testing.T) {
	resp := &RetrieveResponse{
		Evidence: []RetrieveEvidence{
			{
				ID:            "ev-1",
				Type:          "text",
				Source:        "segment",
				DocumentID:    "doc-1",
				DocumentName:  "Doc",
				DatasetID:     "ds-1",
				ProjectID:     "project-1",
				Score:         0.91,
				VfsPath:       "/docs/doc.md",
				StructurePath: "/docs/doc.md#Intro",
				Text: &RetrieveTextEvidence{
					SegmentID: "seg-1",
					Content:   "full content",
					Snippet:   "snippet",
				},
			},
		},
	}

	resp.normalizeResultsFromEvidence()

	if len(resp.Results) != 1 {
		t.Fatalf("expected 1 normalized result, got %d", len(resp.Results))
	}
	got := resp.Results[0]
	if got.ID != "seg-1" {
		t.Fatalf("expected text segment id, got %q", got.ID)
	}
	if got.Content != "full content" || got.Snippet != "snippet" {
		t.Fatalf("unexpected normalized text: content=%q snippet=%q", got.Content, got.Snippet)
	}
	if got.EvidenceType != "text" || got.EvidenceSource != "segment" {
		t.Fatalf("evidence metadata was not preserved: %+v", got)
	}
	if got.DocumentID != "doc-1" || got.DatasetID != "ds-1" || got.ProjectID != "project-1" {
		t.Fatalf("metadata was not preserved: %+v", got)
	}
}
