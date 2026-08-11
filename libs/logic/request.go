package logic

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"thoughtsexport/libs/utils"

	"github.com/marknown/ohttp"
)

type Request struct {
	cookie    string
	firstHash string
}

type NodesResponse struct {
	NextPageToken string  `json:"nextPageToken"`
	Nodes         []*Node `json:"result"`
}

type Node struct {
	ID          string `json:"_id"`
	ParentId    string `json:"_parentId"`
	WorkspaceId string `json:"_workspaceId"`
	Created     time.Time
	Title       string
	Type        string
	WithChild   bool
	Path        string
	Info        struct {
		DownloadUrl string
		FileType    string
		FileName    string
	}
}

type Workspaces struct {
	ID           string `json:"_id"`
	Created      time.Time
	Name         string
	Organization struct {
		ID   string `json:"_id"`
		Name string
	}
	WorkspaceSecurity WorkspaceSecurity
}

type WorkspaceSecurity struct {
	DisableShare      bool
	DisableMove       bool
	DisableOutput     bool
	DisableSharespace bool
}

type WorkspaceSecurityResponse struct {
	ID                string `json:"_id"`
	WorkspaceSecurity WorkspaceSecurity
}

type NodeDownload struct {
	FileType string
	FullPath string
	DownURL  string
}

type Template struct {
	ID                string          `json:"_id"`
	Title             string          `json:"title"`
	RelatedTemplateID string          `json:"relatedTemplateId"`
	ContentID         string          `json:"contentId"`
	SourceContentID   string          `json:"_contentId"`
	Type              string          `json:"type"`
	BoundType         string          `json:"boundType"`
	BoundID           string          `json:"_boundId"`
	Position          float64         `json:"pos"`
	CreatedAt         string          `json:"created"`
	UpdatedAt         string          `json:"updated"`
	SourceOwnerID     string          `json:"_ownerId"`
	Deleted           bool            `json:"isDeleted"`
	Icon              string          `json:"icon,omitempty"`
	Summary           []string        `json:"summary,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

func (t *Template) UnmarshalJSON(data []byte) error {
	type templateAlias Template
	var decoded templateAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*t = Template(decoded)
	t.Raw = append(t.Raw[:0], data...)
	return nil
}

type IDResponse struct {
	ID string `json:"id"`
}

type DownInfoResponse struct {
	ConvertProcess int
	Message        struct {
		DownloadUrl string
		Error       string
	}
}

func NewRequest(cookie string, firstHash string) *Request {
	return &Request{cookie: cookie, firstHash: firstHash}
}

func (r *Request) GetWorkspace(hash string) (*Workspaces, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/api/workspaces/%s?pageSize=1000&_=%d", hash, utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	var result Workspaces
	if err := utils.JSONToStruct(content, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetTemplates returns the templates bound to one workspace. The API has
// returned both a bare array and an object containing result in different
// deployments, so both response shapes are accepted.
func (r *Request) GetTemplates(workspaceID string) ([]Template, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/api/workspaces/%s/templates?_=%d", workspaceID, utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	return decodeTemplates([]byte(content))
}

func (r *Request) GetTemplatePreview(templateID string) (json.RawMessage, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 30 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/api/templates/%s:preview?_=%d", url.PathEscape(templateID), utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(content), &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, errors.New("template preview response has no result")
	}
	return envelope.Result, nil
}

func decodeTemplates(data []byte) ([]Template, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var templates []Template
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &templates); err != nil {
			return nil, err
		}
		return templates, nil
	}
	var envelope struct {
		Result []Template `json:"result"`
		Data   []Template `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if envelope.Result != nil {
		return envelope.Result, nil
	}
	return envelope.Data, nil
}

func (r *Request) EnableOutput(hash string, enable bool) (bool, error) {
	settings := ohttp.InitSetttings()
	settings.Timeout = 10 * time.Second
	settings.IsAajx = true
	settings.Referer = "https://thoughts.teambition.com"
	settings.Cookies = r.cookie
	settings.ContentType = "application/json; charset=utf-8"
	disableStr := "true"
	if enable {
		disableStr = "false"
	}
	params := fmt.Sprintf(`{"optTarget":"disableOutput","optVal":%s}`, disableStr)
	req, err := settings.NewRequest("PUT", fmt.Sprintf("https://thoughts.teambition.com/api/workspaces/%s/workspaceSecurity", hash), params)
	if err != nil {
		return false, err
	}
	resp, err := settings.Do(req)
	if err != nil {
		return false, err
	}
	content, err := resp.ContentString()
	if err != nil {
		return false, err
	}
	var result WorkspaceSecurityResponse
	if err := utils.JSONToStruct(content, &result); err != nil {
		return false, err
	}
	return enable != result.WorkspaceSecurity.DisableOutput, nil
}

func (r *Request) GetAllNodes(hashSpace string, prefixPath string) ([]*Node, error) {
	var allNodes []*Node
	nodes, err := r.GetNodesByHash(hashSpace, prefixPath)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node.WithChild {
			nodes1, err := r.GetAllNodes(node.ID, node.Path)
			if err == nil {
				allNodes = append(allNodes, nodes1...)
			}
		}
		allNodes = append(allNodes, node)
	}
	return allNodes, nil
}

func (r *Request) GetNodesByHash(hash string, prefixPath string) ([]*Node, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	parentHash := ""
	if r.firstHash != hash {
		parentHash = fmt.Sprintf("&_parentId=%s", url.QueryEscape(hash))
	}
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/api/workspaces/%s/nodes?pageSize=1000%s&_=%d", r.firstHash, parentHash, utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	var result NodesResponse
	if err := utils.JSONToStruct(content, &result); err != nil {
		return nil, err
	}
	for _, node := range result.Nodes {
		node.Path = fmt.Sprintf("%s/%s", prefixPath, node.Title)
	}
	return result.Nodes, nil
}

func (r *Request) GetDownloadUrl(hash string, prefixPath string, fileType string) (*NodeDownload, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	dict := map[string]string{"docx": "docx", "html": "zip"}
	if _, ok := dict[fileType]; !ok {
		return nil, errors.New("fileType is invalid")
	}
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/convert/api/nodes/%s/export:%s?pageSize=1000&_=%d", hash, fileType, utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	var result IDResponse
	if err := utils.JSONToStruct(content, &result); err != nil {
		return nil, err
	}
	if result.ID == "" {
		return nil, fmt.Errorf("没有获取到 %s 文档的下载ID", prefixPath)
	}
	pollURL := fmt.Sprintf("https://thoughts.teambition.com/convert/api/exportDocx:polling?pageSize=1000&id=%s&_=%d", result.ID, utils.UnixTimstampMillisecond())
	downInfoResponse, err := r.PollingDownloadUrl(pollURL)
	if err != nil {
		return nil, err
	}
	ext := dict[fileType]
	return &NodeDownload{FileType: "docx", FullPath: prefixPath + "." + ext, DownURL: downInfoResponse.Message.DownloadUrl}, nil
}

func (r *Request) PollingDownloadUrl(url string) (*DownInfoResponse, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	var err error
	var content string
	var result DownInfoResponse
	var maxRetry = 5
	for retryCounter := 1; ; retryCounter++ {
		time.Sleep(1 * time.Second)
		content, _, err = req.Get(url)
		if err != nil {
			break
		}
		if err = utils.JSONToStruct(content, &result); err != nil {
			break
		}
		if result.ConvertProcess == 1 && result.Message.DownloadUrl != "" {
			break
		}
		if result.ConvertProcess == -1 {
			err = fmt.Errorf("获取下载链接失败[%s]", result.Message.Error)
			break
		}
		if retryCounter >= maxRetry {
			err = fmt.Errorf("获取下载链接失败[已尝试%d次]", maxRetry)
			break
		}
	}
	return &result, err
}

func (r *Request) GetDownloadUrlByDetail(hash string, prefixPath string) (*NodeDownload, error) {
	req := ohttp.InitSetttings()
	req.Timeout = 10 * time.Second
	req.IsAajx = true
	req.Referer = "https://thoughts.teambition.com"
	req.Cookies = r.cookie
	content, _, err := req.Get(fmt.Sprintf("https://thoughts.teambition.com/api/workspaces/%s/nodes/%s?pageSize=1000&_=%d", r.firstHash, hash, utils.UnixTimstampMillisecond()))
	if err != nil {
		return nil, err
	}
	var result Node
	if err := utils.JSONToStruct(content, &result); err != nil {
		return nil, err
	}
	if result.Info.DownloadUrl == "" {
		return nil, fmt.Errorf("没有获取到 %s 文档的下载ID", prefixPath)
	}
	return &NodeDownload{FileType: result.Info.FileType, FullPath: prefixPath, DownURL: result.Info.DownloadUrl}, nil
}
