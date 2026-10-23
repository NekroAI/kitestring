package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/store"
)

// 需求 / 决策关联的提交、回滚与 CI（R-156）。纸鸢只关联和展示：不执行 git revert / reset / push，也不触发 CI。
// CI 结果只读：Agent 用 commit_link 上报；Hub 配了 GitHub token（KS_GITHUB_TOKEN 或 GITHUB_TOKEN）时，
// 打开需求时用只读接口查 check-runs；没有 token、仓库不在 GitHub、或 GitHub 上查不到时不显示，也不报错。

type reqCommitsOut struct {
	Body store.ReqCommits
}

func (h *Hub) reqCommits(ctx context.Context, in *refPath) (*reqCommitsOut, error) {
	rc, err := h.store.GetReqCommits(ctx, in.Ref)
	if err != nil {
		return nil, apiErr(err)
	}
	if r, err := h.store.GetRequirement(ctx, in.Ref); err == nil {
		h.refreshCI(r.ProjectID, rc.Commits)
	}
	return &reqCommitsOut{Body: rc}, nil
}

type commitLinkIn struct {
	Body struct {
		SHA         string `json:"sha" doc:"提交哈希（7～40 位）"`
		Requirement string `json:"requirement,omitempty" doc:"关联到哪条需求（R-x）；只上报 CI 时可不填"`
		Subject     string `json:"subject,omitempty" doc:"提交标题（本地还没采集到这个提交时显示用）"`
		CI          string `json:"ci,omitempty" enum:"pending,success,failure" doc:"上报 CI 结果（只读记录，纸鸢不会触发 CI）"`
		CIURL       string `json:"ci_url,omitempty" doc:"CI 结果链接"`
		SessionKey  string `json:"session_key,omitempty"`
	}
}

type commitLinkOut struct {
	Body store.ReqCommits
}

func (h *Hub) linkCommit(ctx context.Context, in *commitLinkIn) (*commitLinkOut, error) {
	b := in.Body
	if store.NormSHA(b.SHA) == "" {
		return nil, huma.Error400BadRequest("sha 要写提交哈希（7～40 位十六进制）：" + b.SHA)
	}
	if b.Requirement == "" && b.CI == "" {
		return nil, huma.Error400BadRequest("要么写 requirement 关联到需求，要么写 ci 上报 CI 结果")
	}
	projectID := ""
	var r store.Requirement
	if b.Requirement != "" {
		var err error
		if r, err = h.store.GetRequirement(ctx, b.Requirement); err != nil {
			return nil, apiErr(err)
		}
		projectID = r.ProjectID
	}
	if projectID == "" && b.SessionKey != "" {
		if s, err := h.store.GetSession(ctx, b.SessionKey); err == nil {
			projectID = s.ProjectID
		}
	}
	if projectID == "" {
		return nil, huma.Error400BadRequest("认不出提交属于哪个项目：请写 requirement，或在项目目录里的会话中调用")
	}
	if b.Requirement != "" {
		if err := h.store.LinkCommit(ctx, projectID, b.SessionKey, b.SHA, b.Subject, r.Code); err != nil {
			return nil, apiErr(err)
		}
	}
	if b.CI != "" {
		if err := h.store.SetCommitCI(ctx, projectID, b.SHA, store.CIState{State: b.CI, URL: strings.TrimSpace(b.CIURL), Source: "agent"}); err != nil {
			return nil, apiErr(err)
		}
	}
	h.broker.publish("requirements")
	out := store.ReqCommits{Commits: []store.ChainCommit{}, Hints: []string{}}
	if b.Requirement != "" {
		var err error
		if out, err = h.store.GetReqCommits(ctx, r.Code); err != nil {
			return nil, apiErr(err)
		}
	}
	return &commitLinkOut{Body: out}, nil
}

func (h *Hub) registerCommitsWeb(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "get-requirement-commits", Method: http.MethodGet, Path: "/api/requirements/{ref}/commits", Tags: []string{"requirements"}, Summary: "需求关联的提交、回滚与 CI 结果（R-156）"}, h.reqCommits)
}

func (h *Hub) registerCommitsAgent(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-get-requirement-commits", Method: http.MethodGet, Path: "/api/agent/requirements/{ref}/commits", Tags: []string{"agent"}, Summary: "需求关联的提交、回滚与 CI 结果（R-156）"}, h.reqCommits)
	huma.Register(api, huma.Operation{OperationID: "agent-link-commit", Method: http.MethodPost, Path: "/api/agent/commits", Tags: []string{"agent"}, Summary: "手动把提交关联到需求，或上报提交的 CI 结果（只记录，不改仓库、不触发 CI，R-156）"}, h.linkCommit)
}

// GitHub 只读查询 CI（check-runs）。githubAPI 可在测试里换成假服务。
var (
	githubAPI  = "https://api.github.com"
	ciFetching sync.Map // 项目 + 哈希 → 正在查
	ciPending  = 2 * time.Minute
)

func githubToken() string {
	if t := strings.TrimSpace(os.Getenv("KS_GITHUB_TOKEN")); t != "" {
		return t
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

// refreshCI 在后台查这些提交在 GitHub 上的 CI 结果：只查完整哈希、Agent 没上报过的；
// 已有通过 / 未通过结论的不再查，进行中或查不到的每 2 分钟最多查一次。没有 token 或仓库不在 GitHub 时什么都不做。
func (h *Hub) refreshCI(projectID string, cs []store.ChainCommit) {
	token := githubToken()
	if token == "" || projectID == "" {
		return
	}
	p, err := h.store.GetProject(context.Background(), projectID)
	if err != nil || !strings.HasPrefix(p.Remote, "github.com/") {
		return
	}
	repo := strings.TrimPrefix(p.Remote, "github.com/")
	for _, c := range cs {
		sha := store.NormSHA(c.Link)
		if len(sha) != 40 {
			continue
		}
		if c.CI != nil && (c.CI.Source == "agent" || c.CI.State != "pending") {
			continue
		}
		at, _, _, err := h.store.CommitCIAt(context.Background(), projectID, sha)
		if err != nil || !at.IsZero() && time.Since(at) < ciPending { // 只查记过的提交（本地采集 / Agent 关联）
			continue
		}
		k := projectID + "/" + sha
		if _, busy := ciFetching.LoadOrStore(k, true); busy {
			continue
		}
		go func() {
			defer ciFetching.Delete(k)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			ci, ok := fetchGitHubCI(ctx, token, repo, sha)
			if !ok {
				_ = h.store.TouchCommitCI(ctx, projectID, sha)
				return
			}
			if h.store.SetCommitCI(ctx, projectID, sha, ci) == nil {
				h.broker.publish("requirements")
			}
		}()
	}
}

// fetchGitHubCI 用只读接口 GET /repos/{repo}/commits/{sha}/check-runs 汇总 CI：有在跑的算进行中，
// 有失败 / 超时 / 取消算未通过，其余（通过、跳过、中性）算通过；没有 check run 或请求失败时返回 false。
func fetchGitHubCI(ctx context.Context, token, repo, sha string) (store.CIState, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubAPI+"/repos/"+repo+"/commits/"+sha+"/check-runs?per_page=100", nil)
	if err != nil {
		return store.CIState{}, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return store.CIState{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return store.CIState{}, false
	}
	var out struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || len(out.CheckRuns) == 0 {
		return store.CIState{}, false
	}
	ci := store.CIState{State: "success", URL: "https://github.com/" + repo + "/commit/" + sha + "/checks", Source: "github", At: time.Now()}
	for _, r := range out.CheckRuns {
		switch {
		case r.Status != "completed":
			if ci.State != "failure" {
				ci.State = "pending"
			}
		case r.Conclusion == "failure" || r.Conclusion == "timed_out" || r.Conclusion == "cancelled" || r.Conclusion == "action_required" || r.Conclusion == "startup_failure":
			ci.State = "failure"
			if r.HTMLURL != "" {
				ci.URL = r.HTMLURL
			}
		}
	}
	return ci, true
}
