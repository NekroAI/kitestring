package blocks

import "time"

// Prototype 是一个可交互原型（R-197）：Agent 交的 HTML 页面加图片等资源，打成 zip 存进 hub。
// 证据、决策、决策选项、要决策的点的选项里都能挂；存的是交的那一版的快照（版本内容不会变）。
// URL 是隔离路径（/p/<令牌>/<入口>），在无来源的沙箱里打开；访客页里换成由访客链接派生的地址。
type Prototype struct {
	ID          string    `json:"id"`
	Lineage     string    `json:"lineage" doc:"同一原型各版本共用的编号（第 1 版的 ID）"`
	Version     int       `json:"version" doc:"第几版（从 1 开始）"`
	Title       string    `json:"title"`
	Entry       string    `json:"entry" doc:"入口页面在包里的路径"`
	Files       int       `json:"files" doc:"包里的文件数"`
	Size        int64     `json:"size" doc:"zip 包大小（字节）"`
	Cover       *File     `json:"cover,omitempty" doc:"封面（Agent 交的截图）；没交时网页显示占位图"`
	URL         string    `json:"url" doc:"打开地址（隔离路径）"`
	DownloadURL string    `json:"download_url" doc:"下载整包的地址"`
	CreatedAt   time.Time `json:"created_at"`
	// Versions：同一原型的全部版本（新的在前），只在需求的证据里给出，用来切换历史版本
	Versions []PrototypeVersion `json:"versions,omitempty"`
}

// PrototypeVersion 是原型的一个历史版本。
type PrototypeVersion struct {
	ID          string    `json:"id"`
	Version     int       `json:"version"`
	Files       int       `json:"files"`
	Size        int64     `json:"size"`
	URL         string    `json:"url"`
	DownloadURL string    `json:"download_url"`
	CreatedAt   time.Time `json:"created_at"`
}

// ProtoResolver 把原型 ID 解析成快照；找不到时 ok = false。
type ProtoResolver func(id string) (p Prototype, ok bool)

// ParsePointsProto 同 ParsePoints，另外认选项里的 prototype（原型 ID）。
func ParsePointsProto(points []any, resolve Resolver, proto ProtoResolver) (out []Block, warnings []string) {
	return parsePoints(points, resolve, proto)
}

// WalkPrototypes 对块里引用的每个原型调用 fn（用来给访客换地址）。
func WalkPrototypes(bs []Block, fn func(*Prototype)) {
	for i := range bs {
		for j := range bs[i].Items {
			if p := bs[i].Items[j].Prototype; p != nil {
				fn(p)
			}
		}
	}
}
