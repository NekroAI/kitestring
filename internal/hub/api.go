package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/NekroAI/kitestring/internal/notify"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/summary"
	"github.com/NekroAI/kitestring/internal/version"
)

// Handler 组装全部 HTTP 路由。web 为前端静态资源处理器，可为 nil。
func (h *Hub) Handler(web http.Handler) http.Handler {
	webMux := http.NewServeMux()
	agentMux := http.NewServeMux()
	publicMux := http.NewServeMux()

	webAPI := humago.New(webMux, apiConfig())
	agentAPI := humago.New(agentMux, apiConfig())
	publicAPI := humago.New(publicMux, apiConfig())
	h.registerPublic(publicAPI)
	h.registerWeb(webAPI)
	h.registerReview(webAPI)
	h.registerPlans(webAPI)
	h.registerAgent(agentAPI)
	h.registerAgentPlans(agentAPI)

	root := http.NewServeMux()
	root.Handle("/api/auth/", publicMux)
	root.Handle("/api/agent/", h.requireDevice(agentMux))
	root.Handle("/api/device/ws", h.requireDevice(http.HandlerFunc(h.serveDeviceWS)))
	root.Handle("/api/stream", h.requireWeb(http.HandlerFunc(h.broker.serve)))
	webUploader := func(*http.Request) string { return "user" }
	devUploader := func(r *http.Request) string { d, _ := deviceFrom(r.Context()); return "device:" + d.ID }
	root.Handle("POST /api/files", h.requireWeb(h.handleUpload(webUploader)))
	root.Handle("GET /api/files/{id}", h.requireWeb(http.HandlerFunc(h.handleDownload)))
	root.Handle("POST /api/agent/files", h.requireDevice(h.handleUpload(devUploader)))
	root.Handle("GET /api/agent/files/{id}", h.requireDevice(http.HandlerFunc(h.handleDownload)))
	root.Handle("/api/", h.requireWeb(webMux))
	if web != nil {
		root.Handle("/", web)
	}
	return withClient(root)
}

func apiConfig() huma.Config {
	c := huma.DefaultConfig("Kitestring Hub API", version.Version)
	c.DocsPath = ""
	c.OpenAPIPath = ""
	c.SchemasPath = ""
	c.CreateHooks = nil // 不在响应里附加 $schema 链接
	return c
}

// OpenAPI 返回完整 API 的 OpenAPI 文档，供前端生成类型（D12）。
func (h *Hub) OpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	api := humago.New(mux, apiConfig())
	h.registerPublic(api)
	h.registerWeb(api)
	h.registerReview(api)
	h.registerPlans(api)
	h.registerAgent(api)
	h.registerAgentPlans(api)
	return api.OpenAPI()
}

func apiErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, store.ErrNotFound) {
		return huma.Error404NotFound(err.Error())
	}
	var se huma.StatusError
	if errors.As(err, &se) {
		return err
	}
	return huma.Error400BadRequest(err.Error())
}

type empty struct{}

type okBody struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func ok() *okBody {
	o := &okBody{}
	o.Body.OK = true
	return o
}

// ---------- 公开接口：登录 ----------

type authStatus struct {
	Body struct {
		Initialized bool   `json:"initialized"`
		LoggedIn    bool   `json:"logged_in"`
		Version     string `json:"version"`
	}
}

type passwordIn struct {
	Body struct {
		Password string `json:"password" minLength:"8"`
	}
}

type loginOut struct {
	SetCookie http.Cookie `header:"Set-Cookie"`
	Body      struct {
		OK bool `json:"ok"`
	}
}

type cookieIn struct {
	Session string `cookie:"ks_session"`
}

func (h *Hub) registerPublic(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "auth-status", Method: http.MethodGet, Path: "/api/auth/status", Tags: []string{"auth"}},
		func(ctx context.Context, in *cookieIn) (*authStatus, error) {
			out := &authStatus{}
			set, err := h.passwordSet(ctx)
			if err != nil {
				return nil, err
			}
			out.Body.Initialized = set
			out.Body.Version = version.Version
			if in.Session != "" {
				out.Body.LoggedIn, _ = h.store.ValidWebSession(ctx, HashToken(in.Session))
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "auth-setup", Method: http.MethodPost, Path: "/api/auth/setup", Tags: []string{"auth"}, Summary: "首次设置密码"},
		func(ctx context.Context, in *passwordIn) (*loginOut, error) {
			set, err := h.passwordSet(ctx)
			if err != nil {
				return nil, err
			}
			if set {
				return nil, huma.Error409Conflict("密码已设置，请登录")
			}
			if err := h.SetPassword(ctx, in.Body.Password); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "auth-login", Method: http.MethodPost, Path: "/api/auth/login", Tags: []string{"auth"}},
		func(ctx context.Context, in *struct {
			Body struct {
				Password string `json:"password"`
			}
		}) (*loginOut, error) {
			if err := h.verifyPassword(ctx, in.Body.Password); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "auth-logout", Method: http.MethodPost, Path: "/api/auth/logout", Tags: []string{"auth"}},
		func(ctx context.Context, in *cookieIn) (*loginOut, error) {
			if in.Session != "" {
				_ = h.store.DeleteWebSession(ctx, HashToken(in.Session), false)
			}
			out := &loginOut{SetCookie: http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true}}
			out.Body.OK = true
			return out, nil
		})
}

func (h *Hub) loginOK(ctx context.Context) (*loginOut, error) {
	c, err := h.newWebSession(ctx)
	if err != nil {
		return nil, err
	}
	out := &loginOut{SetCookie: c}
	out.Body.OK = true
	return out, nil
}

// ---------- Web 接口 ----------

type sessionsOut struct {
	Body struct {
		Sessions []store.Session `json:"sessions"`
		Devices  []store.Device  `json:"devices"`
	}
}

type sessionDetailOut struct {
	Body struct {
		Session store.Session    `json:"session"`
		Events  []protocol.Event `json:"events"`
	}
}

type keyPath struct {
	Key string `path:"key"`
}

type idPath struct {
	ID string `path:"id"`
}

type refPath struct {
	Ref string `path:"ref"`
}

type devicesOut struct {
	Body struct {
		Devices []store.Device `json:"devices"`
	}
}

type deviceCreated struct {
	Body struct {
		Device store.Device `json:"device"`
		Token  string       `json:"token"`
	}
}

type projectsOut struct {
	Body struct {
		Projects   []store.Project   `json:"projects"`
		Workspaces []store.Workspace `json:"workspaces"`
	}
}

type workspaceIn struct {
	Body struct {
		Name   string             `json:"name" minLength:"1"`
		Notify store.NotifyConfig `json:"notify"`
	}
}

type workspaceOut struct {
	Body store.Workspace
}

type projectIn struct {
	ID   string `path:"id"`
	Body struct {
		Name        string `json:"name" minLength:"1"`
		WorkspaceID string `json:"workspace_id,omitempty"`
		DocRoot     string `json:"doc_root"`
		// AutoDispatch：会话空闲时自动送达排给它的待开始需求；不传表示不修改
		AutoDispatch *bool `json:"auto_dispatch,omitempty"`
	}
}

type decisionsOut struct {
	Body struct {
		Decisions []store.Decision `json:"decisions"`
	}
}

type decisionOut struct {
	Body store.Decision
}

type answersIn struct {
	Body struct {
		Answers []struct {
			Ref    string   `json:"ref"`
			Answer string   `json:"answer"`
			Files  []string `json:"files,omitempty" doc:"附图 ID"`
		} `json:"answers" minItems:"1"`
	}
}

type reqsOut struct {
	Body struct {
		Requirements []store.Requirement `json:"requirements"`
		Statuses     []string            `json:"statuses"`
	}
}

type reqOut struct {
	Body store.Requirement
}

// ReqFields 是 Web 与 agent 共用的需求可写字段（导出以便 Huma 展开嵌入字段）。
type ReqFields struct {
	ProjectID  string `json:"project_id,omitempty"`
	Title      string `json:"title" minLength:"1"`
	Detail     string `json:"detail,omitempty"`
	Status     string `json:"status,omitempty" enum:"draft,ready,in_progress,blocked,review,done,shelved"`
	Priority   string `json:"priority,omitempty" enum:"P0,P1,P2,P3"`
	Source     string `json:"source,omitempty"`
	Acceptance string `json:"acceptance,omitempty"`
	DocLink    string `json:"doc_link,omitempty"`
	Kind       string `json:"kind,omitempty" doc:"design = 设计类需求（先交方案、选定后再实现）；feature = 普通；不传表示不修改"`
	// Files 是描述里的附图 ID；不传表示不修改，传空数组表示清空。
	Files []string `json:"files,omitempty"`
}

func (h *Hub) toReq(ctx context.Context, f ReqFields) (store.Requirement, error) {
	r := store.Requirement{ProjectID: f.ProjectID, Title: f.Title, Detail: f.Detail, Status: f.Status, Priority: f.Priority, Source: f.Source, Acceptance: f.Acceptance, DocLink: f.DocLink, Kind: f.Kind}
	if f.Files != nil {
		files, err := h.store.ResolveFiles(ctx, f.Files)
		if err != nil {
			return r, err
		}
		r.Files = files
	}
	return r, nil
}

// SettingsView 是返回给 Web 的配置，隐藏 API key。
type SettingsView struct {
	PublicURL     string             `json:"public_url"`
	AI            AIFeatures         `json:"ai"`
	SummaryBase   string             `json:"summary_base_url"`
	SummaryModel  string             `json:"summary_model"`
	SummaryKeySet bool               `json:"summary_api_key_set"`
	NotifyDefault store.NotifyConfig `json:"notify_default"`
	NotifyEvents  []string           `json:"notify_events"`
}

type settingsIn struct {
	Body struct {
		PublicURL     string             `json:"public_url"`
		AI            AIFeatures         `json:"ai"`
		SummaryBase   string             `json:"summary_base_url"`
		SummaryModel  string             `json:"summary_model"`
		SummaryKey    string             `json:"summary_api_key,omitempty" doc:"留空表示不修改"`
		NotifyDefault store.NotifyConfig `json:"notify_default"`
	}
}

type settingsOut struct {
	Body SettingsView
}

func (h *Hub) settingsView(ctx context.Context) (*settingsOut, error) {
	s, err := h.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return &settingsOut{Body: SettingsView{
		PublicURL: s.PublicURL, AI: s.AI, SummaryBase: s.Summary.BaseURL, SummaryModel: s.Summary.Model,
		SummaryKeySet: s.Summary.APIKey != "", NotifyDefault: s.NotifyDefault, NotifyEvents: notify.AllEvents,
	}}, nil
}

func (h *Hub) registerWeb(api huma.API) {
	tag := func(t string) []string { return []string{t} }

	huma.Register(api, huma.Operation{OperationID: "change-password", Method: http.MethodPost, Path: "/api/password", Tags: tag("auth")},
		func(ctx context.Context, in *struct {
			Body struct {
				Old string `json:"old"`
				New string `json:"new" minLength:"8"`
			}
		}) (*loginOut, error) {
			if err := h.verifyPassword(ctx, in.Body.Old); err != nil {
				var se huma.StatusError
				if errors.As(err, &se) && se.GetStatus() == http.StatusTooManyRequests {
					return nil, err
				}
				return nil, huma.Error400BadRequest("原密码错误")
			}
			if err := h.SetPassword(ctx, in.Body.New); err != nil {
				return nil, err
			}
			return h.loginOK(ctx)
		})

	// 实例墙
	huma.Register(api, huma.Operation{OperationID: "list-sessions", Method: http.MethodGet, Path: "/api/sessions", Tags: tag("sessions")},
		func(ctx context.Context, _ *empty) (*sessionsOut, error) {
			out := &sessionsOut{}
			var err error
			if out.Body.Sessions, err = h.store.ListSessions(ctx, recentWindow); err != nil {
				return nil, err
			}
			out.Body.Devices, err = h.store.ListDevices(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "get-session", Method: http.MethodGet, Path: "/api/sessions/{key}", Tags: tag("sessions")},
		func(ctx context.Context, in *keyPath) (*sessionDetailOut, error) {
			out := &sessionDetailOut{}
			var err error
			if out.Body.Session, err = h.store.GetSession(ctx, in.Key); err != nil {
				return nil, apiErr(err)
			}
			out.Body.Events, err = h.store.SessionEvents(ctx, in.Key, 50)
			return out, err
		})

	huma.Register(api, huma.Operation{OperationID: "mark-session-done", Method: http.MethodPost, Path: "/api/sessions/{key}/done", Tags: tag("sessions"), Summary: "手动标记 / 取消“已完成”（你再给它发消息时自动取消）"},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Done bool `json:"done"`
			}
		}) (*sessionDetailOut, error) {
			if err := h.store.SetSessionDone(ctx, in.Key, in.Body.Done); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("sessions")
			out := &sessionDetailOut{}
			var err error
			out.Body.Session, err = h.store.GetSession(ctx, in.Key)
			out.Body.Events = []protocol.Event{}
			return out, apiErr(err)
		})

	// 设备
	huma.Register(api, huma.Operation{OperationID: "list-devices", Method: http.MethodGet, Path: "/api/devices", Tags: tag("devices")},
		func(ctx context.Context, _ *empty) (*devicesOut, error) {
			out := &devicesOut{}
			var err error
			out.Body.Devices, err = h.store.ListDevices(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-device", Method: http.MethodPost, Path: "/api/devices", Tags: tag("devices"), Summary: "新增设备并返回一次性显示的 token"},
		func(ctx context.Context, in *struct {
			Body struct {
				Name string `json:"name" minLength:"1" maxLength:"64"`
			}
		}) (*deviceCreated, error) {
			tok := NewDeviceToken()
			d, err := h.store.CreateDevice(ctx, strings.TrimSpace(in.Body.Name), HashToken(tok))
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("devices")
			out := &deviceCreated{}
			out.Body.Device, out.Body.Token = d, tok
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "revoke-device", Method: http.MethodPost, Path: "/api/devices/{id}/revoke", Tags: tag("devices")},
		func(ctx context.Context, in *idPath) (*okBody, error) {
			if err := h.store.RevokeDevice(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.mu.Lock()
			if c := h.conns[in.ID]; c != nil {
				c.cancel()
			}
			h.mu.Unlock()
			h.broker.publish("devices", "sessions")
			return ok(), nil
		})

	// 项目与工作空间
	huma.Register(api, huma.Operation{OperationID: "list-projects", Method: http.MethodGet, Path: "/api/projects", Tags: tag("projects")},
		func(ctx context.Context, _ *empty) (*projectsOut, error) {
			out := &projectsOut{}
			var err error
			if out.Body.Projects, err = h.store.ListProjects(ctx); err != nil {
				return nil, err
			}
			out.Body.Workspaces, err = h.store.ListWorkspaces(ctx)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "update-project", Method: http.MethodPut, Path: "/api/projects/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *projectIn) (*okBody, error) {
			cur, err := h.store.GetProject(ctx, in.ID)
			if err != nil {
				return nil, apiErr(err)
			}
			auto := cur.AutoDispatch
			if in.Body.AutoDispatch != nil {
				auto = *in.Body.AutoDispatch
			}
			err = h.store.UpdateProject(ctx, store.Project{ID: in.ID, Name: in.Body.Name, WorkspaceID: in.Body.WorkspaceID, DocRoot: in.Body.DocRoot, AutoDispatch: auto})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects", "sessions", "requirements")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "create-workspace", Method: http.MethodPost, Path: "/api/workspaces", Tags: tag("projects")},
		func(ctx context.Context, in *workspaceIn) (*workspaceOut, error) {
			w, err := h.store.SaveWorkspace(ctx, store.Workspace{Name: in.Body.Name, Notify: in.Body.Notify})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return &workspaceOut{Body: w}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "update-workspace", Method: http.MethodPut, Path: "/api/workspaces/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *struct {
			ID   string `path:"id"`
			Body struct {
				Name   string             `json:"name" minLength:"1"`
				Notify store.NotifyConfig `json:"notify"`
			}
		}) (*workspaceOut, error) {
			w, err := h.store.SaveWorkspace(ctx, store.Workspace{ID: in.ID, Name: in.Body.Name, Notify: in.Body.Notify})
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return &workspaceOut{Body: w}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-workspace", Method: http.MethodDelete, Path: "/api/workspaces/{id}", Tags: tag("projects")},
		func(ctx context.Context, in *idPath) (*okBody, error) {
			if err := h.store.DeleteWorkspace(ctx, in.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("projects")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-notify", Method: http.MethodPost, Path: "/api/notify/test", Tags: tag("projects"), Summary: "向指定渠道配置发送测试通知"},
		func(ctx context.Context, in *struct {
			Body store.NotifyConfig
		}) (*okBody, error) {
			if !notify.HasChannel(in.Body) {
				return nil, huma.Error400BadRequest("未配置任何渠道")
			}
			if err := notify.Send(ctx, in.Body, notify.Message{Title: "Kitestring 测试通知", Body: "如果你看到这条消息，说明通知渠道配置正确。", Group: "kitestring"}); err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			return ok(), nil
		})

	// 决策收件箱
	huma.Register(api, huma.Operation{OperationID: "list-decisions", Method: http.MethodGet, Path: "/api/decisions", Tags: tag("decisions")},
		func(ctx context.Context, in *struct {
			Status []string `query:"status" enum:"pending,answered,delivered,closed"`
		}) (*decisionsOut, error) {
			out := &decisionsOut{}
			var err error
			out.Body.Decisions, err = h.store.ListDecisions(ctx, in.Status, 500)
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "answer-decisions", Method: http.MethodPost, Path: "/api/decisions/answers", Tags: tag("decisions"), Summary: "批量作答"},
		func(ctx context.Context, in *answersIn) (*decisionsOut, error) {
			out := &decisionsOut{Body: struct {
				Decisions []store.Decision `json:"decisions"`
			}{Decisions: []store.Decision{}}}
			devices := map[string]bool{}
			for _, a := range in.Body.Answers {
				files, err := h.store.ResolveFiles(ctx, a.Files)
				if err != nil {
					return nil, err
				}
				d, err := h.store.AnswerDecision(ctx, a.Ref, a.Answer, files)
				if err != nil {
					return nil, apiErr(err)
				}
				out.Body.Decisions = append(out.Body.Decisions, d)
				if x, err := h.store.GetSession(ctx, d.SessionKey); err == nil {
					devices[x.DeviceID] = true
				}
			}
			for id := range devices {
				h.pushDecisions(ctx, id)
			}
			h.broker.publish("decisions", "sessions", "requirements")
			return out, nil
		})
	decisionAction := func(id, path string, fn func(ctx context.Context, ref string, body map[string]string) (store.Decision, error)) {
		huma.Register(api, huma.Operation{OperationID: id, Method: http.MethodPost, Path: "/api/decisions/{ref}/" + path, Tags: tag("decisions")},
			func(ctx context.Context, in *struct {
				Ref  string `path:"ref"`
				Body map[string]string
			}) (*decisionOut, error) {
				d, err := fn(ctx, in.Ref, in.Body)
				if err != nil {
					return nil, apiErr(err)
				}
				h.decisionsChanged(ctx, d)
				return &decisionOut{Body: d}, nil
			})
	}
	decisionAction("reassign-decision", "reassign", func(ctx context.Context, ref string, b map[string]string) (store.Decision, error) {
		if _, err := h.store.GetSession(ctx, b["session_key"]); err != nil {
			return store.Decision{}, errors.New("目标会话不存在")
		}
		prev, err := h.store.GetDecision(ctx, ref)
		if err != nil {
			return prev, err
		}
		d, err := h.store.ReassignDecision(ctx, ref, b["session_key"])
		if err == nil && prev.SessionKey != "" {
			if x, err := h.store.GetSession(ctx, prev.SessionKey); err == nil {
				h.pushDecisions(ctx, x.DeviceID) // 从原设备的缓存中撤下
			}
		}
		return d, err
	})
	decisionAction("resend-decision", "resend", func(ctx context.Context, ref string, _ map[string]string) (store.Decision, error) {
		return h.store.ResendDecision(ctx, ref)
	})
	decisionAction("close-decision", "close", func(ctx context.Context, ref string, _ map[string]string) (store.Decision, error) {
		return h.store.CloseDecision(ctx, ref)
	})

	// 需求看板
	huma.Register(api, huma.Operation{OperationID: "list-requirements", Method: http.MethodGet, Path: "/api/requirements", Tags: tag("requirements")},
		func(ctx context.Context, in *struct {
			ProjectID string   `query:"project_id"`
			Status    []string `query:"status"`
		}) (*reqsOut, error) {
			out := &reqsOut{}
			var err error
			out.Body.Requirements, err = h.store.ListRequirements(ctx, in.ProjectID, in.Status)
			out.Body.Statuses = store.ReqStatuses
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "create-requirement", Method: http.MethodPost, Path: "/api/requirements", Tags: tag("requirements")},
		func(ctx context.Context, in *struct{ Body ReqFields }) (*reqOut, error) {
			r, err := h.toReq(ctx, in.Body)
			if err != nil {
				return nil, err
			}
			r.CreatedBy = "user"
			r, err = h.store.CreateRequirement(ctx, r)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements")
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "get-requirement", Method: http.MethodGet, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *refPath) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "update-requirement", Method: http.MethodPut, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body ReqFields
		}) (*reqOut, error) {
			cur, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			r, err := h.toReq(ctx, in.Body)
			if err != nil {
				return nil, err
			}
			r.ID = cur.ID
			r, err = h.store.UpdateRequirement(ctx, r)
			if err != nil {
				return nil, apiErr(err)
			}
			h.reopenStage(ctx, cur, r.Status)
			h.broker.publish("requirements", "sessions", "plans")
			h.kick(ctx, r.ID)
			if r.Status != cur.Status {
				h.refreshDelivered(ctx, r)
			}
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "delete-requirement", Method: http.MethodDelete, Path: "/api/requirements/{ref}", Tags: tag("requirements")},
		func(ctx context.Context, in *refPath) (*okBody, error) {
			cur, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if err := h.store.DeleteRequirement(ctx, cur.ID); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements", "sessions")
			return ok(), nil
		})
	huma.Register(api, huma.Operation{OperationID: "link-requirement", Method: http.MethodPost, Path: "/api/requirements/{ref}/links", Tags: tag("requirements"), Summary: "手动关联会话（D5）"},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				SessionKey string `json:"session_key"`
				Unlink     bool   `json:"unlink,omitempty"`
			}
		}) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.Unlink {
				err = h.store.UnlinkSession(ctx, r.ID, in.Body.SessionKey)
			} else {
				err = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements", "sessions")
			if !in.Body.Unlink {
				h.kick(ctx, r.ID) // 待开始的需求关联到空闲会话：马上送过去
			}
			r, err = h.store.GetRequirement(ctx, r.ID)
			return &reqOut{Body: r}, apiErr(err)
		})

	// 设置
	huma.Register(api, huma.Operation{OperationID: "get-settings", Method: http.MethodGet, Path: "/api/settings", Tags: tag("settings")},
		func(ctx context.Context, _ *empty) (*settingsOut, error) { return h.settingsView(ctx) })
	huma.Register(api, huma.Operation{OperationID: "update-settings", Method: http.MethodPut, Path: "/api/settings", Tags: tag("settings")},
		func(ctx context.Context, in *settingsIn) (*settingsOut, error) {
			cur, err := h.Settings(ctx)
			if err != nil {
				return nil, err
			}
			b := in.Body
			cur.PublicURL = strings.TrimSuffix(strings.TrimSpace(b.PublicURL), "/")
			cur.AI = b.AI
			cur.Summary.BaseURL = strings.TrimSpace(b.SummaryBase)
			cur.Summary.Model = strings.TrimSpace(b.SummaryModel)
			if b.SummaryKey != "" {
				cur.Summary.APIKey = b.SummaryKey
			}
			cur.NotifyDefault = b.NotifyDefault
			if err := h.SaveSettings(ctx, cur); err != nil {
				return nil, err
			}
			return h.settingsView(ctx)
		})
	huma.Register(api, huma.Operation{OperationID: "recap", Method: http.MethodPost, Path: "/api/recap", Tags: tag("ai"), Summary: "生成一段时间内的进展回顾"},
		func(ctx context.Context, in *recapIn) (*recapOut, error) {
			if !in.Body.To.After(in.Body.From) {
				return nil, huma.Error400BadRequest("时间范围无效")
			}
			text, err := h.Recap(ctx, in.Body.From, in.Body.To)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity(err.Error())
			}
			out := &recapOut{}
			out.Body.Markdown, out.Body.From, out.Body.To = text, in.Body.From, in.Body.To
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "test-summary", Method: http.MethodPost, Path: "/api/settings/test-summary", Tags: tag("settings"),
		Summary: "用一段固定的示例对话调用摘要模型，验证配置是否可用"},
		func(ctx context.Context, _ *empty) (*summaryTestOut, error) {
			s, err := h.Settings(ctx)
			if err != nil {
				return nil, err
			}
			out := &summaryTestOut{}
			out.Body.SamplePrompt = summarySamplePrompt
			out.Body.SampleReply = summarySampleReply
			start := time.Now()
			text, err := summary.Generate(ctx, s.Summary, summarySamplePrompt, summarySampleReply)
			out.Body.LatencyMS = time.Since(start).Milliseconds()
			if err != nil {
				return nil, huma.Error502BadGateway(err.Error())
			}
			out.Body.Summary = text
			return out, nil
		})
}

type recapIn struct {
	Body struct {
		From time.Time `json:"from"`
		To   time.Time `json:"to"`
	}
}

type recapOut struct {
	Body struct {
		Markdown string    `json:"markdown"`
		From     time.Time `json:"from"`
		To       time.Time `json:"to"`
	}
}

const (
	summarySamplePrompt = "给登录页加上“记住我”功能"
	summarySampleReply  = "已在登录表单增加“记住我”复选框，勾选后会话有效期延长到 30 天，并补充了单元测试，全部通过。"
)

type summaryTestOut struct {
	Body struct {
		SamplePrompt string `json:"sample_prompt" doc:"示例：用户最后一条指令"`
		SampleReply  string `json:"sample_reply" doc:"示例：助手最后一条回复"`
		Summary      string `json:"summary" doc:"模型生成的一句话摘要"`
		LatencyMS    int64  `json:"latency_ms"`
	}
}

// ---------- agent 接口（设备 token，经 daemon 转发 MCP / CLI 调用） ----------

type agentDecisionIn struct {
	Body struct {
		SessionKey     string   `json:"session_key,omitempty" doc:"提出决策的会话，由 daemon 填写"`
		Question       string   `json:"question" minLength:"1"`
		Context        string   `json:"context,omitempty"`
		Options        []string `json:"options,omitempty"`
		OptionImages   []string `json:"option_images,omitempty" doc:"每个选项的效果图附件 ID（与 options 一一对应，可留空），先上传到 /api/agent/files"`
		Approval       bool     `json:"approval,omitempty" doc:"请用户批准：选项固定为“批准 / 拒绝”，两种都可以附言"`
		Images         []string `json:"images,omitempty" doc:"问题附带的图（例如待批准的方案图）的附件 ID"`
		Recommendation string   `json:"recommendation,omitempty"`
		Requirement    string   `json:"requirement,omitempty" doc:"关联需求 ID 或 R-编号"`
	}
}

type agentReqIn struct {
	Body struct {
		ReqFields
		SessionKey string `json:"session_key,omitempty"`
	}
}

func (h *Hub) registerAgent(api huma.API) {
	tag := []string{"agent"}
	huma.Register(api, huma.Operation{OperationID: "agent-list-requirements", Method: http.MethodGet, Path: "/api/agent/requirements", Tags: tag},
		func(ctx context.Context, in *struct {
			SessionKey string   `query:"session_key"`
			All        bool     `query:"all" doc:"为真时列出所有项目，否则只列会话所在项目"`
			Status     []string `query:"status"`
		}) (*reqsOut, error) {
			project := ""
			if !in.All && in.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.SessionKey); err == nil {
					project = x.ProjectID
				}
			}
			out := &reqsOut{}
			var err error
			out.Body.Requirements, err = h.store.ListRequirements(ctx, project, in.Status)
			out.Body.Statuses = store.ReqStatuses
			return out, err
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-requirement", Method: http.MethodGet, Path: "/api/agent/requirements/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-create-requirement", Method: http.MethodPost, Path: "/api/agent/requirements", Tags: tag},
		func(ctx context.Context, in *agentReqIn) (*reqOut, error) {
			r, err := h.toReq(ctx, in.Body.ReqFields)
			if err != nil {
				return nil, err
			}
			r.CreatedBy = "agent"
			if r.Status == store.ReqReady {
				r.Status = store.ReqDraft // Agent 记下的需求先停在草稿，你确认后才排进会话的待办（R-66）
			}
			if r.ProjectID == "" && in.Body.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, in.Body.SessionKey); err == nil {
					r.ProjectID = x.ProjectID
				}
			}
			r, err = h.store.CreateRequirement(ctx, r)
			if err != nil {
				return nil, apiErr(err)
			}
			if in.Body.SessionKey != "" {
				_ = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			h.broker.publish("requirements", "sessions")
			r, err = h.store.GetRequirement(ctx, r.ID)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-update-status", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/status", Tags: tag},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Status     string `json:"status" enum:"draft,ready,in_progress,blocked,review,done,shelved"`
				SessionKey string `json:"session_key,omitempty"`
			}
		}) (*reqOut, error) {
			if in.Body.Status == store.ReqDone {
				return nil, huma.Error400BadRequest("“已完成”只能由用户在验收后设置；完成开发请设为 review（待验收）")
			}
			if in.Body.Status == store.ReqReady {
				return nil, huma.Error400BadRequest("“待开始”要由用户确认；你开始做某条需求时用 session_link_req，它会变为进行中")
			}
			before, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			r, err := h.store.SetRequirementStatus(ctx, before.ID, in.Body.Status)
			if err != nil {
				return nil, apiErr(err)
			}
			h.reopenStage(ctx, before, r.Status)
			if in.Body.SessionKey != "" {
				_ = h.store.LinkSession(ctx, r.ID, in.Body.SessionKey)
			}
			h.refreshDelivered(ctx, r)
			h.broker.publish("requirements", "sessions", "plans")
			if r.Status == store.ReqReview {
				h.sendNotify(r.ProjectID, notify.Message{Event: notify.EventRequirementReview, Title: "待验收 · " + r.Code, Body: r.Title, Group: "kitestring"}, "/requirements")
			}
			return &reqOut{Body: r}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-attach-evidence", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/evidence", Tags: tag},
		func(ctx context.Context, in *struct {
			Ref  string `path:"ref"`
			Body struct {
				Kind       string   `json:"kind" doc:"例：test、screenshot、commit、doc、manual"`
				Summary    string   `json:"summary" minLength:"1"`
				Link       string   `json:"link,omitempty"`
				Files      []string `json:"files,omitempty" doc:"截图等附图 ID（先上传到 /api/agent/files）"`
				SessionKey string   `json:"session_key,omitempty"`
			}
		}) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Ref)
			if err != nil {
				return nil, apiErr(err)
			}
			files, err := h.store.ResolveFiles(ctx, in.Body.Files)
			if err != nil {
				return nil, err
			}
			// 标成截图 / 录屏却没附文件，用户验收时会看不到任何东西：直接拒绝，让 agent 补上附件。
			switch strings.ToLower(in.Body.Kind) {
			case "screenshot", "video", "recording":
				if len(files) == 0 {
					return nil, huma.Error400BadRequest("证据类型为 " + in.Body.Kind + " 时必须附上图片或录屏（MCP 用 images / videos，命令行用 --image / --video）")
				}
			}
			if _, err := h.store.AddEvidence(ctx, store.Evidence{RequirementID: r.ID, SessionKey: in.Body.SessionKey, Kind: in.Body.Kind, Summary: in.Body.Summary, Link: in.Body.Link, Files: files}); err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("requirements")
			r, err = h.store.GetRequirement(ctx, r.ID)
			return &reqOut{Body: r}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-raise-decision", Method: http.MethodPost, Path: "/api/agent/decisions", Tags: tag},
		func(ctx context.Context, in *agentDecisionIn) (*decisionOut, error) {
			b := in.Body
			d := store.Decision{SessionKey: b.SessionKey, Question: b.Question, Context: b.Context, Options: b.Options, Recommendation: b.Recommendation}
			if d.Options == nil {
				d.Options = []string{}
			}
			if b.Approval {
				d.Approval = true
				d.Options = []string{"批准", "拒绝"}
				b.OptionImages = nil
			}
			if len(b.Images) > 0 {
				fs, err := h.store.ResolveFiles(ctx, b.Images)
				if err != nil {
					return nil, err
				}
				d.ContextFiles = fs
			}
			if len(b.OptionImages) > 0 {
				// 方案对比：每个选项最多一张效果图
				d.OptionFiles = make([][]store.FileRef, len(d.Options))
				for i := range d.Options {
					d.OptionFiles[i] = []store.FileRef{}
					if i < len(b.OptionImages) && b.OptionImages[i] != "" {
						fs, err := h.store.ResolveFiles(ctx, []string{b.OptionImages[i]})
						if err != nil {
							return nil, err
						}
						d.OptionFiles[i] = fs
					}
				}
			}
			if b.SessionKey != "" {
				if x, err := h.store.GetSession(ctx, b.SessionKey); err == nil {
					d.ProjectID = x.ProjectID
				}
			}
			if b.Requirement != "" {
				r, err := h.store.GetRequirement(ctx, b.Requirement)
				if err != nil {
					return nil, huma.Error400BadRequest("关联需求不存在：" + b.Requirement)
				}
				d.RequirementID = r.ID
				if d.ProjectID == "" {
					d.ProjectID = r.ProjectID
				}
				if b.SessionKey != "" {
					_ = h.store.LinkSession(ctx, r.ID, b.SessionKey)
				}
			}
			d, err := h.store.CreateDecision(ctx, d)
			if err != nil {
				return nil, apiErr(err)
			}
			h.broker.publish("decisions", "sessions", "requirements")
			go h.aiOnDecision(d)
			h.sendNotify(d.ProjectID, notify.Message{Event: notify.EventDecisionRaised, Title: "待决策 · " + d.Code, Body: d.Question, Group: "kitestring"}, "/decisions")
			return &decisionOut{Body: d}, nil
		})
	huma.Register(api, huma.Operation{OperationID: "agent-get-decision", Method: http.MethodGet, Path: "/api/agent/decisions/{ref}", Tags: tag},
		func(ctx context.Context, in *refPath) (*decisionOut, error) {
			d, err := h.store.GetDecision(ctx, in.Ref)
			return &decisionOut{Body: d}, apiErr(err)
		})
	huma.Register(api, huma.Operation{OperationID: "agent-link-session", Method: http.MethodPost, Path: "/api/agent/sessions/{key}/link", Tags: tag},
		func(ctx context.Context, in *struct {
			Key  string `path:"key"`
			Body struct {
				Requirement string `json:"requirement"`
			}
		}) (*reqOut, error) {
			r, err := h.store.GetRequirement(ctx, in.Body.Requirement)
			if err != nil {
				return nil, apiErr(err)
			}
			if _, err := h.store.GetSession(ctx, in.Key); err != nil {
				return nil, huma.Error404NotFound("会话尚未上报，稍后重试")
			}
			if err := h.store.LinkSession(ctx, r.ID, in.Key); err != nil {
				return nil, apiErr(err)
			}
			// Agent 关联会话就是开始做了：还在草稿 / 待开始的需求自动进入进行中（R-35）
			if r.Status == store.ReqDraft || r.Status == store.ReqReady {
				if _, err := h.store.SetRequirementStatus(ctx, r.ID, store.ReqInProgress); err != nil {
					return nil, apiErr(err)
				}
			}
			h.broker.publish("requirements", "sessions")
			r, err = h.store.GetRequirement(ctx, r.ID)
			return &reqOut{Body: r}, apiErr(err)
		})
}
