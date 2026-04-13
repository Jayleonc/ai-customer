package router

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.pinquest.cn/ai-customer/internal/khclient"
	"git.pinquest.cn/ai-customer/internal/model"
	"git.pinquest.cn/ai-customer/internal/store"
	"git.pinquest.cn/ai-customer/internal/wecom"
	"github.com/gin-gonic/gin"
)

type groupListItem struct {
	GroupID        string
	GroupName      string
	StatusLabel    string
	ReplyRobotText string
}

type robotOption struct {
	ID    string
	Label string
}

type configPageData struct {
	GroupItems         []groupListItem
	Selected           *model.EnterpriseGroup
	SelectedGroupID    string
	SelectedRobotSet   map[string]bool
	SelectedDatasetSet map[string]bool
	SelectedFeatureSet map[string]bool
	SelectedReplyRobot string
	CustomFeatureKeys  string
	RobotOptions       []robotOption
	DatasetOptions     []khclient.DatasetItem
	FeatureOptions     []string
	Saved              bool
	Error              string
	DatasetLoadError   string
	RobotSyncError     string
	RobotSyncInfo      string
}

var configPageTemplate = template.Must(template.New("config-db-page").Funcs(template.FuncMap{
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return "-"
		}
		return t.Format("2006-01-02 15:04:05")
	},
}).Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>群配置中心</title>
  <style>
    :root {
      --bg: #f3f6f2;
      --surface: #ffffff;
      --text-main: #13211a;
      --text-sub: #5b6a60;
      --line: #dbe3db;
      --accent: #1f7a46;
      --accent-soft: #e8f4ec;
      --warn-bg: #fff8e6;
      --warn-line: #f0d58d;
      --warn-text: #915f00;
      --danger: #b42318;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      font-family: "Noto Sans SC", "IBM Plex Sans", "PingFang SC", "Microsoft YaHei", sans-serif;
      color: var(--text-main);
      background:
        radial-gradient(950px 450px at 8% -10%, #e7efe6 0%, transparent 60%),
        radial-gradient(920px 460px at 92% -16%, #ebf2ee 0%, transparent 58%),
        var(--bg);
    }
    .wrap {
      max-width: 1200px;
      margin: 0 auto;
      padding: 22px 18px 36px;
    }
    .head {
      margin-bottom: 14px;
      display: flex;
      align-items: flex-start;
      justify-content: space-between;
      gap: 12px;
      flex-wrap: wrap;
    }
    .title {
      margin: 0;
      font-size: 28px;
      font-weight: 700;
    }
    .desc {
      margin: 8px 0 0;
      font-size: 14px;
      color: var(--text-sub);
    }
    .sync-link {
      text-decoration: none;
      border: 1px solid #b7c8bb;
      border-radius: 10px;
      color: #245a37;
      font-size: 13px;
      padding: 8px 12px;
      background: #f8fbf9;
    }
    .msg {
      margin: 0 0 12px;
      padding: 10px 12px;
      border-radius: 10px;
      font-size: 13px;
    }
    .ok { border: 1px solid #b9e2c8; background: var(--accent-soft); color: #0b5c2c; }
    .err { border: 1px solid #f4c9c6; background: #fef3f2; color: var(--danger); }
    .warn { border: 1px solid var(--warn-line); background: var(--warn-bg); color: var(--warn-text); }
    .layout {
      display: grid;
      grid-template-columns: 320px 1fr;
      gap: 14px;
      align-items: start;
    }
    .panel {
      border: 1px solid var(--line);
      border-radius: 14px;
      background: var(--surface);
      overflow: hidden;
    }
    .panel-h {
      padding: 14px;
      border-bottom: 1px solid var(--line);
      font-size: 13px;
      color: var(--text-sub);
      font-weight: 600;
    }
    .group-list {
      max-height: 72vh;
      overflow: auto;
    }
    .group-item {
      display: block;
      padding: 12px 14px;
      border-bottom: 1px solid #eef2ee;
      color: inherit;
      text-decoration: none;
      transition: background 0.15s ease;
    }
    .group-item:hover { background: #f7faf7; }
    .group-item.active {
      background: #edf7f0;
      border-left: 3px solid var(--accent);
      padding-left: 11px;
    }
    .group-name {
      font-size: 14px;
      font-weight: 600;
      line-height: 1.4;
      word-break: break-word;
    }
    .group-meta {
      margin-top: 5px;
      font-size: 12px;
      color: var(--text-sub);
      display: flex;
      justify-content: space-between;
      gap: 8px;
    }
    .row {
      padding: 14px;
      border-top: 1px solid var(--line);
    }
    .row:first-child { border-top: none; }
    .label {
      display: block;
      margin-bottom: 7px;
      font-size: 14px;
      font-weight: 600;
    }
    .hint {
      margin: 0 0 9px;
      font-size: 12px;
      color: var(--text-sub);
      line-height: 1.5;
    }
    .grid2 {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 10px;
    }
    .kv {
      border: 1px solid #e7eee7;
      border-radius: 10px;
      background: #fbfdfb;
      padding: 10px;
      font-size: 13px;
      line-height: 1.45;
      word-break: break-word;
    }
    textarea, select, input[type="text"] {
      width: 100%;
      border: 1px solid #c8d3c8;
      border-radius: 10px;
      padding: 9px 11px;
      font-size: 14px;
      color: var(--text-main);
      font-family: inherit;
      background: #fff;
    }
    textarea { min-height: 96px; resize: vertical; line-height: 1.55; }
    select[multiple] { min-height: 160px; }
    textarea:focus, select:focus, input[type="text"]:focus {
      outline: none;
      border-color: var(--accent);
      box-shadow: 0 0 0 3px rgba(31, 122, 70, 0.12);
    }
    .feature-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
      gap: 8px;
    }
    .pick-grid {
      border: 1px solid #d6e1d7;
      border-radius: 12px;
      background: #fcfdfc;
      padding: 10px;
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
      gap: 8px;
      max-height: 220px;
      overflow: auto;
    }
    .pick-item {
      display: flex;
      align-items: center;
      gap: 8px;
      border: 1px solid #d9e4db;
      border-radius: 10px;
      padding: 8px 10px;
      background: #fff;
      cursor: pointer;
      transition: border-color 0.15s ease, background 0.15s ease, transform 0.15s ease;
      font-size: 13px;
      line-height: 1.4;
      word-break: break-word;
    }
    .pick-item:hover {
      border-color: #b7d1be;
      transform: translateY(-1px);
    }
    .pick-item input {
      accent-color: var(--accent);
      width: 16px;
      height: 16px;
      margin: 0;
      flex: 0 0 auto;
    }
    .pick-item.active {
      border-color: #7dbc92;
      background: #ebf7ef;
      box-shadow: inset 0 0 0 1px rgba(31, 122, 70, 0.12);
    }
    .feature-item {
      border: 1px solid #dde6de;
      border-radius: 10px;
      padding: 8px 10px;
      font-size: 13px;
      background: #fcfdfc;
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .foot {
      padding: 14px;
      border-top: 1px solid var(--line);
      background: #fafcfa;
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }
    .meta {
      font-size: 12px;
      color: var(--text-sub);
      line-height: 1.5;
    }
    .btn {
      border: 0;
      border-radius: 10px;
      background: var(--accent);
      color: #fff;
      cursor: pointer;
      font-size: 14px;
      font-weight: 600;
      padding: 10px 18px;
    }
    @media (max-width: 960px) {
      .layout { grid-template-columns: 1fr; }
      .group-list { max-height: 34vh; }
    }
  </style>
</head>
<body>
  <main class="wrap">
    <header class="head">
      <div>
        <h1 class="title">群配置中心</h1>
        <p class="desc">运营可视化配置：选择群、选择机器人、选择知识库、配置提示词。</p>
      </div>
      <a class="sync-link" href="/config?group_id={{.SelectedGroupID}}&sync_robot=1">同步机器人列表</a>
    </header>

    {{if .Saved}}<p class="msg ok">配置已保存。</p>{{end}}
    {{if .RobotSyncInfo}}<p class="msg ok">{{.RobotSyncInfo}}</p>{{end}}
    {{if .Error}}<p class="msg err">{{.Error}}</p>{{end}}
    {{if .RobotSyncError}}<p class="msg warn">{{.RobotSyncError}}</p>{{end}}
    {{if .DatasetLoadError}}<p class="msg warn">{{.DatasetLoadError}}</p>{{end}}

    <section class="layout">
      <aside class="panel">
        <div class="panel-h">群列表（enterprise_group）</div>
        <div class="group-list">
          {{if .GroupItems}}
            {{range .GroupItems}}
            <a class="group-item {{if eq $.SelectedGroupID .GroupID}}active{{end}}" href="/config?group_id={{.GroupID}}">
              <div class="group-name">{{.GroupName}}</div>
              <div class="group-meta">
                <span>{{.GroupID}}</span>
                <span>{{.StatusLabel}}</span>
              </div>
              <div class="group-meta">
                <span>回复机器人</span>
                <span>{{.ReplyRobotText}}</span>
              </div>
            </a>
            {{end}}
          {{else}}
            <div style="padding: 14px; color: var(--text-sub); font-size: 13px;">暂无群数据</div>
          {{end}}
        </div>
      </aside>

      <section class="panel">
        {{if .Selected}}
          <form method="post" action="/config">
            <input type="hidden" name="group_id" value="{{.Selected.GroupID}}" />

            <div class="row">
              <div class="grid2">
                <div class="kv"><strong>群 ID</strong><br />{{.Selected.GroupID}}</div>
                <div class="kv"><strong>群名</strong><br />{{if .Selected.GroupName}}{{.Selected.GroupName}}{{else}}-{{end}}</div>
                <div class="kv"><strong>客户</strong><br />{{if .Selected.CustomerName}}{{.Selected.CustomerName}}{{else}}-{{end}}</div>
                <div class="kv"><strong>群主</strong><br />{{if .Selected.OwnerID}}{{.Selected.OwnerID}}{{else}}-{{end}}</div>
              </div>
            </div>

            <div class="row">
              <label class="label" for="status">群状态</label>
              <select id="status" name="status">
                <option value="1" {{if eq .Selected.Status 1}}selected{{end}}>正常</option>
                <option value="2" {{if eq .Selected.Status 2}}selected{{end}}>解散</option>
              </select>
            </div>

            <div class="row">
              <label class="label" for="robot_ids">可用机器人（多选）</label>
              <p class="hint">按机器人名称选择。未选择的机器人不会在该群回复。</p>
              <div id="robot_ids" class="pick-grid">
                {{range .RobotOptions}}
                  <label class="pick-item {{if index $.SelectedRobotSet .ID}}active{{end}}" data-kind="robot" data-id="{{.ID}}" data-label="{{.Label}}">
                    <input type="checkbox" name="robot_ids" value="{{.ID}}" {{if index $.SelectedRobotSet .ID}}checked{{end}} />
                    <span>{{.Label}}</span>
                  </label>
                {{end}}
              </div>
            </div>

            <div class="row">
              <label class="label" for="reply_robot_id">默认回复机器人</label>
              <p class="hint">当群里配置了多个机器人时，指定由谁回复；留空表示按当前触发机器人处理。</p>
              <select id="reply_robot_id" name="reply_robot_id" data-current="{{.SelectedReplyRobot}}">
                <option value="">未指定（按当前触发机器人）</option>
                {{range .RobotOptions}}
                  <option value="{{.ID}}" {{if eq $.SelectedReplyRobot .ID}}selected{{end}}>{{.Label}}</option>
                {{end}}
              </select>
            </div>

            <div class="row">
              <label class="label" for="dataset_ids">知识库（多选）</label>
              <p class="hint">候选来自 knowledge-hub：<code>POST /api/dataset/list</code>。</p>
              <div id="dataset_ids" class="pick-grid">
                {{range .DatasetOptions}}
                  <label class="pick-item {{if index $.SelectedDatasetSet .ID}}active{{end}}">
                    <input type="checkbox" name="dataset_ids" value="{{.ID}}" {{if index $.SelectedDatasetSet .ID}}checked{{end}} />
                    <span>{{.Name}} ({{.ID}})</span>
                  </label>
                {{end}}
              </div>
            </div>

            <div class="row">
              <label class="label" for="system_prompt">群系统提示词</label>
              <textarea id="system_prompt" name="system_prompt" placeholder="为空时使用系统默认 Prompt">{{.Selected.SystemPrompt}}</textarea>
            </div>

            <div class="row">
              <label class="label">功能开通（feature_tag）</label>
              <p class="hint">按勾选保存为 JSON 布尔值。可在下方新增自定义功能 key。</p>
              <div class="feature-grid">
                {{range .FeatureOptions}}
                  <label class="feature-item">
                    <input type="checkbox" name="feature_enabled" value="{{.}}" {{if index $.SelectedFeatureSet .}}checked{{end}} />
                    <span>{{.}}</span>
                  </label>
                {{end}}
              </div>
              <p class="hint" style="margin-top:8px;">新增功能 key（逗号分隔，新增项默认勾选）</p>
              <input type="text" name="feature_custom_keys" value="{{.CustomFeatureKeys}}" placeholder="例如：vip_support,advanced_export" />
            </div>

            <div class="foot">
              <div class="meta">
                <div>创建时间：{{fmtTime .Selected.CreatedAt}}</div>
                <div>更新时间：{{fmtTime .Selected.UpdatedAt}}</div>
              </div>
              <button class="btn" type="submit">保存到数据库</button>
            </div>
          </form>
        {{else}}
          <div class="row"><p style="margin:0; font-size:14px; color:var(--text-sub);">请选择左侧群进行配置。</p></div>
        {{end}}
      </section>
    </section>
  </main>

  <script>
    (function () {
      var robotMulti = document.getElementById('robot_ids');
      var replySelect = document.getElementById('reply_robot_id');
      if (!robotMulti || !replySelect) return;

      function bindActiveState(root) {
        var items = root.querySelectorAll('.pick-item');
        for (var i = 0; i < items.length; i++) {
          (function (item) {
            var input = item.querySelector('input[type="checkbox"]');
            if (!input) return;
            function sync() {
              if (input.checked) item.classList.add('active');
              else item.classList.remove('active');
            }
            input.addEventListener('change', sync);
            sync();
          })(items[i]);
        }
      }

      function buildReplyOptions() {
        var current = replySelect.getAttribute('data-current') || '';
        var selected = [];
        var all = [];

        var checks = robotMulti.querySelectorAll('label[data-kind="robot"]');
        for (var i = 0; i < checks.length; i++) {
          var item = checks[i];
          var checkbox = item.querySelector('input[type="checkbox"]');
          if (!checkbox) continue;
          var entry = {
            value: item.getAttribute('data-id') || checkbox.value,
            text: item.getAttribute('data-label') || item.textContent.trim()
          };
          all.push(entry);
          if (checkbox.checked) selected.push(entry);
        }
        var source = selected.length > 0 ? selected : all;

        var hasCurrent = false;

        for (var k = 0; k < replySelect.options.length; k++) {
          var opt = replySelect.options[k];
          if (opt.value === '') continue;
          var enabled = false;
          for (var j = 0; j < source.length; j++) {
            if (source[j].value === opt.value) {
              enabled = true;
              break;
            }
          }
          opt.disabled = !enabled;
          opt.hidden = !enabled;
          if (enabled && opt.value === current) {
            hasCurrent = true;
          }
        }
        if (!hasCurrent) replySelect.value = '';
      }

      bindActiveState(robotMulti);
      var datasetRoot = document.getElementById('dataset_ids');
      if (datasetRoot) bindActiveState(datasetRoot);
      buildReplyOptions();
      robotMulti.addEventListener('change', buildReplyOptions, true);
    })();
  </script>
</body>
</html>`))

func registerConfigPageRoutes(
	r *gin.Engine,
	groupStore *store.GroupStore,
	robotStore *store.RobotStore,
	khClient *khclient.Client,
	wecomClient *wecom.Client,
) {
	r.GET("/", func(c *gin.Context) {
		c.Redirect(http.StatusFound, "/config")
	})

	r.GET("/config", func(c *gin.Context) {
		ctx := c.Request.Context()
		selectedGroupID := strings.TrimSpace(c.Query("group_id"))

		var syncErr error
		syncInfo := ""
		if c.Query("sync_robot") == "1" {
			count, err := syncRobotsFromPlatform(ctx, wecomClient, robotStore)
			if err != nil {
				syncErr = err
			} else {
				syncInfo = fmt.Sprintf("机器人同步完成，共刷新 %d 条。", count)
			}
		}

		data := loadConfigPageData(ctx, groupStore, robotStore, khClient, selectedGroupID)
		data.Saved = c.Query("saved") == "1"
		if syncErr != nil {
			data.RobotSyncError = "机器人同步失败：" + syncErr.Error()
		}
		if syncInfo != "" {
			data.RobotSyncInfo = syncInfo
		}
		renderConfigPage(c, data)
	})

	r.POST("/config", func(c *gin.Context) {
		ctx := c.Request.Context()
		groupID := strings.TrimSpace(c.PostForm("group_id"))
		data := loadConfigPageData(ctx, groupStore, robotStore, khClient, groupID)

		if groupID == "" {
			data.Error = "group_id 不能为空"
			overridePageDataByForm(&data, nil, c)
			renderConfigPage(c, data)
			return
		}

		group, err := groupStore.GetAnyByGroupID(ctx, groupID)
		if err != nil {
			data.Error = "查询群配置失败：" + err.Error()
			overridePageDataByForm(&data, nil, c)
			renderConfigPage(c, data)
			return
		}

		nextGroup, validateErr := buildGroupFromForm(group, c, data.FeatureOptions)
		if validateErr != nil {
			data.Error = validateErr.Error()
			overridePageDataByForm(&data, nextGroup, c)
			renderConfigPage(c, data)
			return
		}

		if err := groupStore.Upsert(ctx, nextGroup); err != nil {
			data.Error = "保存失败：" + err.Error()
			overridePageDataByForm(&data, nextGroup, c)
			renderConfigPage(c, data)
			return
		}

		c.Redirect(http.StatusFound, "/config?group_id="+url.QueryEscape(groupID)+"&saved=1")
	})
}

func loadConfigPageData(
	ctx context.Context,
	groupStore *store.GroupStore,
	robotStore *store.RobotStore,
	khClient *khclient.Client,
	selectedGroupID string,
) configPageData {
	data := configPageData{
		SelectedRobotSet:   map[string]bool{},
		SelectedDatasetSet: map[string]bool{},
		SelectedFeatureSet: map[string]bool{},
	}

	groups, err := groupStore.ListAll(ctx)
	if err != nil {
		data.Error = "读取 enterprise_group 失败：" + err.Error()
		return data
	}

	robots, err := robotStore.ListAll(ctx)
	if err != nil {
		data.Error = "读取 robot 失败：" + err.Error()
		return data
	}

	robotNameMap := make(map[string]string, len(robots))
	for _, r := range robots {
		name := strings.TrimSpace(r.Name)
		if name == "" {
			name = r.RobotID
		}
		status := "离线"
		if r.LoginStatus == 1 {
			status = "在线"
		}
		label := fmt.Sprintf("%s（%s）", name, status)
		data.RobotOptions = append(data.RobotOptions, robotOption{ID: r.RobotID, Label: label})
		robotNameMap[r.RobotID] = name
	}

	for _, g := range groups {
		title := strings.TrimSpace(g.GroupName)
		if title == "" {
			title = g.GroupID
		}
		status := "未知"
		if g.Status == 1 {
			status = "正常"
		} else if g.Status == 2 {
			status = "解散"
		}
		replyName := "未指定"
		if g.RobotID != "" {
			replyName = robotNameMap[g.RobotID]
			if replyName == "" {
				replyName = g.RobotID
			}
		}
		data.GroupItems = append(data.GroupItems, groupListItem{
			GroupID:        g.GroupID,
			GroupName:      title,
			StatusLabel:    status,
			ReplyRobotText: replyName,
		})
	}

	if selectedGroupID == "" && len(groups) > 0 {
		selectedGroupID = groups[0].GroupID
	}
	data.SelectedGroupID = selectedGroupID

	for i := range groups {
		if groups[i].GroupID == selectedGroupID {
			data.Selected = &groups[i]
			break
		}
	}

	data.FeatureOptions = collectFeatureOptions(groups)

	if data.Selected != nil {
		data.SelectedRobotSet = toSet(data.Selected.RobotIDs)
		data.SelectedDatasetSet = toSet(data.Selected.DatasetIDs)
		data.SelectedReplyRobot = strings.TrimSpace(data.Selected.RobotID)
		featureMap := parseFeatureTagMap(data.Selected.FeatureTag)
		data.SelectedFeatureSet = toBoolSet(featureMap)
	}

	if khClient == nil {
		data.DatasetLoadError = "knowledge-hub client 未初始化"
		return data
	}
	datasets, err := khClient.ListDatasets(ctx)
	if err != nil {
		data.DatasetLoadError = "知识库列表获取失败：" + err.Error()
		return data
	}
	data.DatasetOptions = datasets
	return data
}

func buildGroupFromForm(origin *model.EnterpriseGroup, c *gin.Context, knownFeatureOptions []string) (*model.EnterpriseGroup, error) {
	group := *origin

	status, err := strconv.Atoi(strings.TrimSpace(c.PostForm("status")))
	if err != nil {
		return &group, fmt.Errorf("status 非法：%w", err)
	}
	if status != 1 && status != 2 {
		return &group, fmt.Errorf("status 仅支持 1(正常) 或 2(解散)")
	}

	robotIDs := uniqueIDs(c.PostFormArray("robot_ids"))
	replyRobotID := strings.TrimSpace(c.PostForm("reply_robot_id"))
	if replyRobotID != "" && !contains(robotIDs, replyRobotID) {
		return &group, fmt.Errorf("默认回复机器人必须在“可用机器人”中")
	}

	datasetIDs := uniqueIDs(c.PostFormArray("dataset_ids"))
	systemPrompt := strings.TrimSpace(c.PostForm("system_prompt"))

	featureJSON, err := buildFeatureTagJSON(
		origin.FeatureTag,
		knownFeatureOptions,
		c.PostFormArray("feature_enabled"),
		c.PostForm("feature_custom_keys"),
	)
	if err != nil {
		return &group, err
	}

	group.Status = status
	group.RobotIDs = robotIDs
	group.RobotID = replyRobotID
	if replyRobotID == "" {
		group.RobotMemberID = ""
	}
	group.DatasetIDs = datasetIDs
	group.SystemPrompt = systemPrompt
	group.FeatureTag = featureJSON

	return &group, nil
}

func overridePageDataByForm(data *configPageData, base *model.EnterpriseGroup, c *gin.Context) {
	if base == nil {
		base = &model.EnterpriseGroup{}
	}
	draft := *base

	draft.GroupID = strings.TrimSpace(c.PostForm("group_id"))
	draft.RobotIDs = uniqueIDs(c.PostFormArray("robot_ids"))
	draft.RobotID = strings.TrimSpace(c.PostForm("reply_robot_id"))
	draft.DatasetIDs = uniqueIDs(c.PostFormArray("dataset_ids"))
	draft.SystemPrompt = strings.TrimSpace(c.PostForm("system_prompt"))

	statusRaw := strings.TrimSpace(c.PostForm("status"))
	if statusRaw != "" {
		if v, err := strconv.Atoi(statusRaw); err == nil {
			draft.Status = v
		}
	}
	if draft.Status == 0 {
		draft.Status = 1
	}

	data.Selected = &draft
	data.SelectedGroupID = draft.GroupID
	data.SelectedRobotSet = toSet(draft.RobotIDs)
	data.SelectedDatasetSet = toSet(draft.DatasetIDs)
	data.SelectedReplyRobot = draft.RobotID
	data.CustomFeatureKeys = strings.TrimSpace(c.PostForm("feature_custom_keys"))
	data.SelectedFeatureSet = toSetFromArray(c.PostFormArray("feature_enabled"))
}

func renderConfigPage(c *gin.Context, data configPageData) {
	var out bytes.Buffer
	if err := configPageTemplate.Execute(&out, data); err != nil {
		c.String(http.StatusInternalServerError, "render config page failed: %v", err)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", out.Bytes())
}

func syncRobotsFromPlatform(ctx context.Context, wc *wecom.Client, robotStore *store.RobotStore) (int, error) {
	if wc == nil {
		return 0, fmt.Errorf("wecom client 未初始化")
	}
	if robotStore == nil {
		return 0, fmt.Errorf("robot store 未初始化")
	}
	list, err := wc.SyncGetRobotList(ctx, nil)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, item := range list {
		if strings.TrimSpace(item.RobotID) == "" {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = strings.TrimSpace(item.NickName)
		}
		loginStatus := item.LoginStatus
		if loginStatus == 0 {
			loginStatus = 2
		}
		if err := robotStore.UpsertFromSync(ctx, &model.Robot{
			RobotID:     item.RobotID,
			Name:        name,
			Avatar:      item.Avatar,
			Phone:       item.Phone,
			Email:       item.Email,
			LoginStatus: loginStatus,
		}); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func collectFeatureOptions(groups []model.EnterpriseGroup) []string {
	defaults := []string{"customer_service", "billing", "invoice", "order", "refund", "api", "report"}
	set := map[string]struct{}{}
	for _, k := range defaults {
		set[k] = struct{}{}
	}
	for _, g := range groups {
		for k := range parseFeatureTagMap(g.FeatureTag) {
			set[k] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func buildFeatureTagJSON(originRaw string, knownOptions, checked []string, customKeysRaw string) (string, error) {
	base := parseFeatureTagMap(originRaw)
	if base == nil {
		base = map[string]any{}
	}

	checkedSet := toSetFromArray(checked)
	customKeys := parseCustomFeatureKeys(customKeysRaw)
	allKeys := uniqueStrings(append(append([]string{}, knownOptions...), customKeys...))

	for _, k := range allKeys {
		base[k] = checkedSet[k]
	}
	for _, k := range customKeys {
		base[k] = true
	}

	b, err := json.Marshal(base)
	if err != nil {
		return "", fmt.Errorf("feature_tag 序列化失败：%w", err)
	}
	return string(b), nil
}

func parseFeatureTagMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func toBoolSet(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k, v := range m {
		switch tv := v.(type) {
		case bool:
			out[k] = tv
		case float64:
			out[k] = tv != 0
		case string:
			out[k] = strings.EqualFold(strings.TrimSpace(tv), "true") || strings.TrimSpace(tv) == "1"
		}
	}
	return out
}

func parseCustomFeatureKeys(raw string) []string {
	replaced := strings.ReplaceAll(raw, "，", ",")
	replaced = strings.ReplaceAll(replaced, "\n", ",")
	parts := strings.Split(replaced, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		k := strings.TrimSpace(p)
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	return uniqueStrings(out)
}

func uniqueIDs(items []string) []string {
	return uniqueStrings(items)
}

func uniqueStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s := strings.TrimSpace(item)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}

func toSet(list []string) map[string]bool {
	set := make(map[string]bool, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		set[item] = true
	}
	return set
}

func toSetFromArray(list []string) map[string]bool {
	return toSet(list)
}
