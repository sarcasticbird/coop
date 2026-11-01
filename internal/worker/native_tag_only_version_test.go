package worker

import (
	"archive/tar"
	"encoding/json"
	"strings"
	"testing"
	"time"

	c "github.com/sarcasticbird/coop/internal/coordination"
)

func tagOnlyVersionBundles(t *testing.T, tag string) ([]byte, []byte) {
	t.Helper()
	entries := []qaBundleEntry{{name: "root/cmd/coop/main.go", content: "package main\nvar version = \"dev\"\nfunc main() {}\n", mode: 0644}, {name: "root/scripts/coop", content: "#!/bin/sh\nexec coop-bin\n", mode: 0755}, {name: "root/internal/library.go", content: "package library\n", mode: 0644}, {name: "root/bin/coop", kind: tar.TypeSymlink, link: "../scripts/coop", mode: 0777}}
	source := qaBundle(t, entries...)
	entries[0].content = strings.Replace(entries[0].content, `"dev"`, `"`+tag+`"`, 1)
	return source, qaBundle(t, entries...)
}

func renewTagOnlyReports(t *testing.T, cat *NativeCatalog, h *c.Handoff) {
	t.Helper()
	for _, actor := range []string{"hawk", "wren"} {
		body, _ := json.Marshal(c.TagOnlyReport{Binding: c.TagOnlyReportBinding(h.Input), Result: "ready"})
		id := "tag-report-" + actor
		cat.Reports[id] = c.Message{ID: id, AuthorID: actor, AuthorKind: "agent", ThreadID: cat.Goals[0].Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: string(body)}
		if actor == "hawk" {
			h.Input.TagOnly.EngineeringReportID, h.Input.TagOnly.EngineeringReportBodySHA = id, BodyHash(string(body))
		} else {
			h.Input.TagOnly.QAReportID, h.Input.TagOnly.QAReportBodySHA = id, BodyHash(string(body))
		}
	}
	cat.Handoffs[0] = *h
}

func tagOnlyVersionFixture(t *testing.T) (Config, NativeCatalog, c.Handoff, []byte, []byte) {
	t.Helper()
	cfg, cat, h := tagOnlyFixture(t)
	source, version := tagOnlyVersionBundles(t, h.Input.Tag)
	sourceSHA, _ := sourceBundleSHA(source)
	versionSHA, _ := sourceBundleSHA(version)
	r := &h.Input.TagOnly
	r.PayloadSchema, r.SourceBundleSHA, r.VersionBundleSHA = c.TagOnlyPayloadV1, sourceSHA, versionSHA
	r.PayloadSHA, _ = tagOnlyPayloadSHA(source, r.PayloadSchema, h.Input.Tag, false)
	r.VersionPRURL, r.VersionHeadSHA, r.VersionCommitSHA, r.VersionTreeSHA, r.VersionBaseSHA = "https://github.com/code/repo/pull/10", strings.Repeat("c", 40), strings.Repeat("d", 40), strings.Repeat("e", 40), r.IntegratedCommitSHA
	task := cat.Tasks[0]
	task.Task.URL, task.Task.Title, task.Task.Body = "https://github.com/code/repo/issues/10", "Version-only update", "Set the exact tag version source without other payload changes"
	task.Task.BodySHA = TaskScopeHash(task.Task.Title, task.Task.Body)
	task.Binding.TaskURL, task.Binding.TaskScopeSHA, task.Binding.PolicySHA = task.Task.URL, task.Task.BodySHA, r.ExecutionPolicySHA
	p := task.PRs[0]
	p.URL, p.TaskURL, p.AssignmentSHA, p.HeadSHA, p.ScopeRevision, p.BaseSHA = r.VersionPRURL, task.Task.URL, NativeAssignmentSHA(task.Binding), r.VersionHeadSHA, BodyHash("version-scope"), r.VersionBaseSHA
	p.QAArtifact = &QAArtifact{Kind: "source", HeadSHA: p.HeadSHA, SHA256: versionSHA, Revision: "source-bundle-v1", URL: "https://api.github.com/repos/code/repo/tarball/" + p.HeadSHA}
	p.Reviews = append([]Review(nil), p.Reviews...)
	for i := range p.Reviews {
		if p.Reviews[i].Author.ID == task.Binding.PeerID {
			p.Reviews[i].ID, p.Reviews[i].HeadSHA = "version-peer", p.HeadSHA
			p.Reviews[i].Body = NativePeerMetadata(NativeReviewResult{Version: 3, Goal: task.Binding.Goal, TaskURL: task.Task.URL, AssignmentSHA: p.AssignmentSHA, HeadSHA: p.HeadSHA, PRScopeRevision: p.ScopeRevision, Verdict: "approved"})
		} else {
			var qa NativeQAResult
			if nativeMetadata(p.Reviews[i].Body, "qa", &qa) != nil {
				t.Fatal("invalid source QA fixture")
			}
			qa.TaskURL, qa.AssignmentSHA, qa.HeadSHA, qa.PRScopeRevision, qa.ArtifactSHA = task.Task.URL, p.AssignmentSHA, p.HeadSHA, p.ScopeRevision, versionSHA
			qa.EvidenceMessageID, qa.EvidenceBodySHA = "version-qa-report", BodyHash("version source QA passed")
			p.Reviews[i].ID, p.Reviews[i].HeadSHA, p.Reviews[i].Body = "version-qa", p.HeadSHA, NativeQAMetadata(qa)
			cat.Reports[qa.EvidenceMessageID] = c.Message{ID: qa.EvidenceMessageID, AuthorID: cat.Goals[0].Actors.QA.PrincipalID, AuthorKind: "agent", ThreadID: cat.Goals[0].Revision.Proposal.QA.EvidenceDestination.ThreadID, Body: "version source QA passed"}
		}
	}
	task.PRs = []PR{p}
	cat.Tasks = append(cat.Tasks, task)
	v := h.Input
	v.Kind, v.Tag, v.TagOnly, v.RequestID = "deliver", "", c.TagOnlyAuthority{}, "version-delivery"
	v.TaskURL, v.PRURL, v.AssignmentSHA, v.TaskScopeSHA, v.BaseSHA, v.HeadSHA, v.PRScopeRevision, v.ArtifactSHA = task.Task.URL, p.URL, p.AssignmentSHA, task.Task.BodySHA, r.VersionBaseSHA, p.HeadSHA, p.ScopeRevision, versionSHA
	v.PeerReviewID, v.QAReviewID, v.QAEvidenceMessageID, v.QAEvidenceBodySHA = "version-peer", "version-qa", "version-qa-report", BodyHash("version source QA passed")
	delivery := c.Handoff{ID: "version-delivery", Sequence: 3, ActorID: "owner", ActorKind: "owner", CreatedAt: time.Now(), Input: v}
	cat.Handoffs = append(cat.Handoffs, delivery)
	r.VersionGoal, r.VersionTaskURL, r.VersionDeliveryHandoffID, r.VersionDeliveryInputSHA = task.Binding.Goal, task.Task.URL, delivery.ID, tagOnlyInputSHA(v)
	r.VersionCompletionRequestID = "version-complete"
	// The source digest is part of its original immutable retained delivery.
	cat.Handoffs[1].Input.Retained.PayloadSHA = sourceSHA
	r.SourceDeliveryInputSHA = tagOnlyInputSHA(cat.Handoffs[1].Input)
	renewTagOnlyReports(t, &cat, &h)
	if c.ValidateHandoff(h.Input) != nil {
		t.Fatal("invalid version authority")
	}
	return cfg, cat, h, source, version
}

func TestTagOnlyVersionRequiresTrustedNativeChain(t *testing.T) {
	for _, change := range []string{"none", "missing-version-task", "version-policy", "version-assignment", "version-peer", "version-qa", "version-evidence", "version-owner", "version-revoked", "source-missing", "source-input", "source-revoked", "old-source-policy", "tag-acceptance", "tag-environment", "renewed-report", "gate-capability"} {
		t.Run(change, func(t *testing.T) {
			cfg, cat, h, _, _ := tagOnlyVersionFixture(t)
			switch change {
			case "missing-version-task":
				cat.Tasks = cat.Tasks[:1]
			case "version-policy":
				cat.Tasks[1].Binding.PolicySHA = BodyHash("drift")
			case "version-assignment":
				cat.Tasks[1].PRs[0].AssignmentSHA = BodyHash("drift")
			case "version-peer":
				cat.Tasks[1].PRs[0].Reviews[0].State = "DISMISSED"
			case "version-qa":
				cat.Tasks[1].PRs[0].Reviews[1].State = "DISMISSED"
			case "version-evidence":
				delete(cat.Reports, "version-qa-report")
			case "version-owner":
				cat.Handoffs[2].ActorKind = "agent"
			case "version-revoked":
				next := cat.Handoffs[2]
				next.ID, next.Sequence, next.Input.Revoked = "withdraw-version", 4, true
				cat.Handoffs = append(cat.Handoffs, next)
			case "source-missing":
				cat.Handoffs[1].ID = "missing"
			case "source-input":
				cat.Handoffs[1].Input.BaseSHA = strings.Repeat("f", 40)
			case "source-revoked":
				next := cat.Handoffs[1]
				next.ID, next.Sequence, next.Input.Revoked = "withdraw-source", 5, true
				cat.Handoffs = append(cat.Handoffs, next)
			case "old-source-policy":
				cat.Handoffs[1].Input.Retained.ExecutionPolicySHA = BodyHash("original execution policy, not current tag policy")
				h.Input.TagOnly.SourceDeliveryInputSHA = tagOnlyInputSHA(cat.Handoffs[1].Input)
				renewTagOnlyReports(t, &cat, &h)
			case "tag-acceptance":
				cat.Handoffs[0].Input.TagOnly.TagAcceptanceSHA = BodyHash("drift")
			case "tag-environment":
				cat.Handoffs[0].Input.TagOnly.TagEnvironmentRevision = "drift"
			case "renewed-report":
				delete(cat.Reports, h.Input.TagOnly.QAReportID)
			case "gate-capability":
				cfg.Team.Members[4].Capabilities = nil
			}
			got := NativeTagOnlyCandidates(cfg, cat, nil, time.Now())
			if change == "none" || change == "old-source-policy" {
				if len(got) != 1 {
					t.Fatal("trusted version chain rejected", change)
				}
			} else if len(got) != 0 {
				t.Fatal("untrusted version/source chain admitted", change)
			}
		})
	}
}

func TestTagOnlyPayloadEquivalence(t *testing.T) {
	_, _, h, source, version := tagOnlyVersionFixture(t)
	if err := VerifyTagOnlyPayload(h.Input, source, source, version, version); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"library", "executable", "mode", "link", "extra", "removed", "wrong-version", "version-file-code", "version-file-mode", "fake-declaration", "arbitrary-schema", "raw-source-digest", "raw-version-digest"} {
		t.Run(change, func(t *testing.T) {
			entries := []qaBundleEntry{{name: "root/cmd/coop/main.go", content: "package main\nvar version = \"" + h.Input.Tag + "\"\nfunc main() {}\n", mode: 0644}, {name: "root/scripts/coop", content: "#!/bin/sh\nexec coop-bin\n", mode: 0755}, {name: "root/internal/library.go", content: "package library\n", mode: 0644}, {name: "root/bin/coop", kind: tar.TypeSymlink, link: "../scripts/coop", mode: 0777}}
			bad := h.Input
			switch change {
			case "library":
				entries[2].content += "var altered = true\n"
			case "executable":
				entries[1].content += "malicious\n"
			case "mode":
				entries[1].mode = 0644
			case "link":
				entries[3].link = "../scripts/foreign"
			case "extra":
				entries = append(entries, qaBundleEntry{name: "root/internal/new.go", content: "package library\n", mode: 0644})
			case "removed":
				entries = entries[:3]
			case "wrong-version":
				entries[0].content = strings.ReplaceAll(entries[0].content, h.Input.Tag, "v9.9.9")
			case "version-file-code":
				entries[0].content += "func injected() {}\n"
			case "version-file-mode":
				entries[0].mode = 0755
			case "fake-declaration":
				entries[0].content = "package main\nvar version=\"dev\"\nvar data = `\nvar version = \"" + h.Input.Tag + "\"\n`\n"
			case "arbitrary-schema":
				bad.TagOnly.PayloadSchema = "ignore-all-go-files"
			case "raw-source-digest":
				bad.TagOnly.SourceBundleSHA = BodyHash("drift")
			case "raw-version-digest":
				bad.TagOnly.VersionBundleSHA = BodyHash("drift")
			}
			altered := qaBundle(t, entries...)
			if err := VerifyTagOnlyPayload(bad, source, source, version, altered); err == nil {
				t.Fatal("payload drift accepted")
			}
		})
	}
}

func TestTagOnlyVersionProductionTarget(t *testing.T) {
	for _, change := range []string{"none", "unmerged-version", "version-head", "version-merge", "version-tree", "version-parent", "version-repository", "version-ancestry", "original-integration-archive", "version-integration-archive", "signed-wrong-version", "signed-library-change"} {
		t.Run(change, func(t *testing.T) {
			_, cat, h, source, version := tagOnlyVersionFixture(t)
			r := h.Input.TagOnly
			repo := cat.Tasks[0].Task.Repo
			if change == "signed-wrong-version" || change == "signed-library-change" {
				content := "package main\nvar version = \"" + h.Input.Tag + "\"\nfunc main() {}\n"
				library := "package library\n"
				if change == "signed-wrong-version" {
					content = strings.ReplaceAll(content, h.Input.Tag, "v9.9.9")
				} else {
					library += "var changed = true\n"
				}
				version = qaBundle(t, qaBundleEntry{name: "root/cmd/coop/main.go", content: content, mode: 0644}, qaBundleEntry{name: "root/scripts/coop", content: "#!/bin/sh\nexec coop-bin\n", mode: 0755}, qaBundleEntry{name: "root/internal/library.go", content: library, mode: 0644}, qaBundleEntry{name: "root/bin/coop", kind: tar.TypeSymlink, link: "../scripts/coop", mode: 0777})
				h.Input.TagOnly.VersionBundleSHA, _ = sourceBundleSHA(version)
			}
			api := tagOnlyReadAPI(func(endpoint string) ([]byte, error) {
				var result any
				switch {
				case strings.Contains(endpoint, "/pulls/"):
					_, n, _ := parseRef(h.Input.PRURL)
					versionPR := !strings.HasSuffix(endpoint, "/"+stringID(n))
					head, merge, merged, repository := h.Input.HeadSHA, r.IntegratedCommitSHA, true, repo
					if versionPR {
						head, merge = r.VersionHeadSHA, r.VersionCommitSHA
						if change == "unmerged-version" {
							merged = false
						}
						if change == "version-head" {
							head = strings.Repeat("f", 40)
						}
						if change == "version-merge" {
							merge = strings.Repeat("f", 40)
						}
						if change == "version-repository" {
							repository = "foreign/repo"
						}
					}
					result = map[string]any{"merged": merged, "merge_commit_sha": merge, "head": map[string]string{"sha": head}, "base": map[string]any{"ref": h.Input.BaseBranch, "repo": map[string]string{"full_name": repository}}}
				case strings.Contains(endpoint, "/git/commits/"):
					sha := strings.TrimPrefix(endpoint, "repos/"+repo+"/git/commits/")
					tree, parent := r.SourceTreeSHA, h.Input.BaseSHA
					if sha == r.VersionHeadSHA || sha == r.VersionCommitSHA {
						tree, parent = r.VersionTreeSHA, r.VersionBaseSHA
						if change == "version-tree" {
							tree = strings.Repeat("f", 40)
						}
						if change == "version-parent" {
							parent = strings.Repeat("f", 40)
						}
					}
					result = map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}, "parents": []map[string]string{{"sha": parent}}}
				case strings.Contains(endpoint, "/compare/"):
					behind := 0
					if change == "version-ancestry" && strings.Contains(endpoint, r.VersionCommitSHA+"...") {
						behind = 1
					}
					result = map[string]any{"status": "ahead", "behind_by": behind}
				case strings.Contains(endpoint, "/tarball/"):
					sha := strings.TrimPrefix(endpoint, "repos/"+repo+"/tarball/")
					if sha == r.VersionHeadSHA || sha == r.VersionCommitSHA {
						if change == "version-integration-archive" && sha == r.VersionCommitSHA {
							return source, nil
						}
						return version, nil
					}
					if change == "original-integration-archive" && sha == r.IntegratedCommitSHA {
						return version, nil
					}
					return source, nil
				default:
					t.Fatal("unexpected production source request", endpoint)
				}
				return json.Marshal(result)
			})
			target, err := nativeTagOnlyTarget(t.Context(), api, repo, h)
			if change == "none" {
				if err != nil || target.SHA != r.VersionCommitSHA || target.Repository != repo {
					t.Fatalf("target=%+v err=%v", target, err)
				}
			} else if err == nil {
				t.Fatal("unverified version target admitted", change)
			}
		})
	}
}

func TestTagOnlyVersionEffectsBindFinalIntegratedCommit(t *testing.T) {
	cfg, cat, h, _, _ := tagOnlyVersionFixture(t)
	target := releaseTarget{Repository: cat.Tasks[0].Task.Repo, SHA: h.Input.TagOnly.VersionCommitSHA}
	record := nativeActionRecord{Version: 3, JobID: "version-job", RequestID: "version-request", InputSHA: BodyHash("version input"), Operation: "tag-only"}
	api := &tagOnlyAPI{t: t, cfg: cfg, intent: newTagOnlyIntent(cfg, h, target, record), lost: "ref"}
	if _, err := publishNativeTagOnly(t.Context(), cfg, api, h, target, record, func() error { return nil }); err == nil {
		t.Fatal("expected lost acknowledgment")
	}
	result, err := publishNativeTagOnly(t.Context(), cfg, api, h, target, record, func() error { return nil })
	if err != nil || result.Phase != "completed" || len(api.writes) != 2 {
		t.Fatalf("result=%+v writes=%v err=%v", result, api.writes, err)
	}
	j, err := readTagOnlyIntent(cfg, h.ID)
	if err != nil || j.Binding.Target.SHA != h.Input.TagOnly.VersionCommitSHA || j.Binding.Target.SHA == h.Input.TagOnly.IntegratedCommitSHA {
		t.Fatal("tag did not bind final version integration", err)
	}
}

func TestTagOnlyHistoricalReadBridgePreservesOrdinaryGuards(t *testing.T) {
	for _, change := range []string{"none", "missing-tag", "missing-source", "owner-body", "owner-imported", "assignment", "source-proof", "current-policy", "non-rook", "revoked"} {
		t.Run(change, func(t *testing.T) {
			cfg, cat, h := tagOnlyFixture(t)
			binding := cat.Tasks[0].Binding
			original := NativeAssignmentSHA(binding)
			if validateNativeRoster(cfg, binding) == nil {
				t.Fatal("fixture did not retain a historical policy")
			}
			switch change {
			case "missing-tag":
				cat.Handoffs = cat.Handoffs[1:]
			case "missing-source":
				cat.Handoffs = cat.Handoffs[:1]
			case "owner-body":
				m := cat.Reports[h.Input.EvidenceMessageID]
				m.Body += "drift"
				cat.Reports[m.ID] = m
			case "owner-imported":
				m := cat.Reports[h.Input.EvidenceMessageID]
				m.Imported = true
				cat.Reports[m.ID] = m
			case "assignment":
				binding.TaskScopeSHA = BodyHash("drift")
			case "source-proof":
				cat.Handoffs[1].Input.Retained.SourceTreeSHA = strings.Repeat("f", 40)
			case "current-policy":
				cat.Handoffs[0].Input.TagOnly.ExecutionPolicySHA = BodyHash("drift")
			case "non-rook":
				cfg.Master.AgentID = "hawk"
			case "revoked":
				cat.Handoffs[0].Input.Revoked = true
			}
			if nativeTagOnlyReadRoster(cfg, cat, binding) != (change == "none") {
				t.Fatal("incorrect read bridge admission", change)
			}
			if change == "none" && (NativeAssignmentSHA(binding) != original || validateNativeRoster(cfg, binding) == nil) {
				t.Fatal("bridge migrated or weakened the ordinary source binding")
			}
		})
	}
}

func TestTagOnlyBinaryAndLibraryPayloadEquivalence(t *testing.T) {
	_, _, h, _, _ := tagOnlyVersionFixture(t)
	entries := []qaBundleEntry{
		{name: "root/cmd/coop/main.go", content: "package main\nvar version = \"dev\"\nfunc main() {}\n", mode: 0644},
		{name: "root/bin/coop", content: "\x7fELF\x00\x01\xffexecutable\x00", mode: 0755},
		{name: "root/lib/libcoop.so", content: "\x7fELF\x00\xfe\x00library\x00", mode: 0644},
	}
	source := qaBundle(t, entries...)
	entries[0].content = strings.Replace(entries[0].content, `"dev"`, `"`+h.Input.Tag+`"`, 1)
	version := qaBundle(t, entries...)
	h.Input.TagOnly.SourceBundleSHA, _ = sourceBundleSHA(source)
	h.Input.TagOnly.VersionBundleSHA, _ = sourceBundleSHA(version)
	h.Input.TagOnly.PayloadSHA, _ = tagOnlyPayloadSHA(source, c.TagOnlyPayloadV1, h.Input.Tag, false)
	if err := VerifyTagOnlyPayload(h.Input, source, source, version, version); err != nil {
		t.Fatal("equal executable/library bytes rejected", err)
	}
	for _, change := range []string{"executable-bytes", "executable-mode", "library-bytes", "library-mode"} {
		t.Run(change, func(t *testing.T) {
			altered := append([]qaBundleEntry(nil), entries...)
			switch change {
			case "executable-bytes":
				altered[1].content += "\x00"
			case "executable-mode":
				altered[1].mode = 0644
			case "library-bytes":
				altered[2].content += "\xff"
			case "library-mode":
				altered[2].mode = 0755
			}
			bundle := qaBundle(t, altered...)
			in := h.Input
			// Even independently reviewed bytes cannot widen the owner-approved
			// equivalence contract or silently exclude executable/library changes.
			in.TagOnly.VersionBundleSHA, _ = sourceBundleSHA(bundle)
			if err := VerifyTagOnlyPayload(in, source, source, bundle, bundle); err == nil {
				t.Fatal("binary/library drift accepted")
			}
		})
	}
}
