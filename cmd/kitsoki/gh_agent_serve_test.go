package main

import (
	"testing"
)

func TestWebhookMentionIssueComment(t *testing.T) {
	body := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"o/r"},
	  "issue":{
	    "number":42,
	    "title":"button crashes",
	    "html_url":"https://github.com/o/r/issues/42",
	    "labels":[{"name":"bug"}]
	  },
	  "comment":{
	    "body":"@kitsoki please fix this",
	    "html_url":"https://github.com/o/r/issues/42#issuecomment-1",
	    "user":{"login":"alice"}
	  }
	}`)
	mention, labels, ok, err := webhookMention(body, "", "@kitsoki")
	if err != nil {
		t.Fatalf("webhookMention: %v", err)
	}
	if !ok {
		t.Fatal("webhookMention ignored a matching comment")
	}
	if mention.Repo != "o/r" {
		t.Fatalf("Repo=%q", mention.Repo)
	}
	if mention.Item.Kind != "issue" || mention.Item.Number != "42" {
		t.Fatalf("Item=%+v", mention.Item)
	}
	if mention.OriginRef != "github:o/r/issue/42" {
		t.Fatalf("OriginRef=%q", mention.OriginRef)
	}
	if len(labels) != 1 || labels[0] != "bug" {
		t.Fatalf("labels=%v", labels)
	}
}

func TestWebhookMentionPullRequestFromIssueComment(t *testing.T) {
	body := []byte(`{
	  "action":"created",
	  "repository":{"full_name":"o/r"},
	  "issue":{
	    "number":7,
	    "title":"change the renderer",
	    "html_url":"https://github.com/o/r/pull/7",
	    "pull_request":{}
	  },
	  "comment":{"body":"Could @kitsoki handle review feedback?","user":{"login":"alice"}}
	}`)
	mention, _, ok, err := webhookMention(body, "", "@kitsoki")
	if err != nil {
		t.Fatalf("webhookMention: %v", err)
	}
	if !ok {
		t.Fatal("webhookMention ignored a matching PR comment")
	}
	if mention.Item.Kind != "pr" || mention.OriginRef != "github:o/r/pr/7" {
		t.Fatalf("mention=%+v", mention)
	}
}

func TestWebhookMentionIgnoresNonMention(t *testing.T) {
	body := []byte(`{"repository":{"full_name":"o/r"},"issue":{"number":1},"comment":{"body":"plain comment"}}`)
	_, _, ok, err := webhookMention(body, "", "@kitsoki")
	if err != nil {
		t.Fatalf("webhookMention: %v", err)
	}
	if ok {
		t.Fatal("non-mention webhook should be ignored")
	}
}
