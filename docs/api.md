# HTTP契约

所有写入场景的JSON入口拒绝未知字段和尾随内容，最大8MiB。服务返回JSON错误；404为对象不存在，409为版本或生命周期冲突，429为成像容量不足，400为不符合输入契约，504为超时。未知路由由HTTP框架返回404。

| 方法与路径 | 输入及结果 |
|---|---|
| GET /healthz | ready |
| POST /v1/plates | id,title,document →201版面 |
| GET /v1/plates/{id} | 完整稿本历史 |
| PUT /v1/plates/{id} | expected_revision,document →追加稿本 |
| POST /v1/plates/{id}/branch | revision,id,title →201独立版面 |
| POST /v1/plates/{id}/archive | 禁止修订；历史仍可读取 |
| POST /v1/plates/{id}/render?revision=1&format=png | 201成品元数据；format也可为svg |
| POST /v1/plates/{id}/exposures?revision=1&format=png | 202瞬态任务 |
| GET /v1/exposures/{id} | queued/running/succeeded/failed/cancelled |
| DELETE /v1/exposures/{id} | 202取消请求 |
| GET /v1/artifacts/{id} | 图片；ETag为成品字节摘要；X-Scene-Digest为稿本摘要 |
| DELETE /v1/artifacts/{id} | 撤销成品 |
| POST /v1/proofs | id,artifact →201 open校样 |
| PUT /v1/proofs/{id} | state为accepted或rejected，note为说明 |
| GET /v1/plates/{id}/bundle | 含完整稿本历史与冻结组件闭包的封包JSON |
| POST /v1/bundles | 原样封包；201导入，重名409 |

## 组件引用

图层的 `component` 字段把其他版面的一份指定稿本作为可复用图形挂入当前版面；该层不再声明 `shapes`。

```json
{"id": "badge", "opacity": 1, "blend": "normal",
 "component": {"plate": "badge-src", "revision": 2, "transform": [-1, 0, 0, 1, 96, 0]}}
```

写入稿本时服务把 `plate`+`revision` 解析为该稿本内容的摘要，存入 `digest` 并把整份文档复制进快照。此后来源版面再修订、归档或改名都不影响已交付版面；`digest` 单独出现时（如分支或导入所得）直接按内容解析，不依赖接收端存在同名版面。同一文档按摘要只存一份。

`transform` 是挂载变换，子文档的局部坐标因此保持原义：嵌套裁域、蒙版、渐变轴与形状变换都在子坐标内解释，镜像和缩放只改变最终落点。PNG 与 SVG 使用同一展开结果；SVG 为每个挂载实例分配独立渐变与裁域 id，且不引用来源版面名称。

拒绝结果：来源版面或稿本不存在 404；`digest` 与指名稿本不符、或已冻结摘要缺失 409/400；挂载当前正在写入的稿本、摘要间循环、嵌套超过 8 层、闭包超过 128 份文档、展开超过 1024 个形状或 64 个实例，均为 400 且不写入任何历史。封包导入时逐份校验组件摘要与场景准入，篡改或与本地同摘要内容冲突即整体拒绝。


成品响应中的data为null，避免重复传输二进制；GET成品获得实际内容。If-None-Match精确匹配强ETag时返回304。成功校样可从持久化快照确认，当前版本没有校样列表/详情接口。GET版面返回完整历史，服务没有全局列表接口。封包 schema 为 `prism-plate-bundle-v2`，`components` 按内容摘要携带全部冻结稿本，可离线重现；旧版仅含版面的封包按不支持的 schema 拒绝。
