# G03：核心预算与协议消费者集成

本文件在各worker独立接受后合并集中使用文档，随后在真实合流SHA执行G03检查；不自引用未来commit，不冒称检查已通过。

| Task | 固定交付SHA | 必需与附加最终检查 |
|---|---|---|
| T10 | `fcb3abb091ac24a5494153e3af92d85437f975ba` | T10-V01=passed, T10-V02=passed, T10-V03=passed, T10-V04=passed |
| T11 | `6bc3a3e14cea19b0f51470d2e91d43d26a921069` | T11-V01=passed, T11-V02=passed, T11-RACE=passed, T11-VET=passed |
| T12 | `e327be37adf57e253f3fa3f7135956786403868b` | T12-V01=passed, T12-RACE=passed, T12-VET=passed, T12-V03=passed, T12-V04=passed |
| T13 | `930869611aade6c79c168b8bb89aa2f8233b7828` | T13-V01=passed, T13-V02=passed, T13-EXTRA-VET=passed |

T10独立review由C02负责预算/选因、C01负责R013审批快照、C04负责append/Inspect/helper/golden；T11由C03、T12由C04、T13由C01固定归档复验。所有初版/返修失败和独立fixture、hash保存在外部alignment-execution目录。

集中合并：README五语言、API reference、run-policy、streaming、A2A、public-errors、Driver streaming contract、docs地图、根godoc、AGENTS及CHANGELOG。选项/Policy/错误和Query/Store用法同批说明；明确Finalize封账、parent原因及原cause、审批描述/应答权、A2A旧正文兼容/新字段验证和opaque key，记录Merge/AGUI ID可见行为变化。

根AST人工核对新增仅C02/C04冻结声明，With函数26→27；R013/R014无新公共API。Driver golden增加Append capability/request/error及active failure code；无新顶层依赖、无generated/schema手改。

W11-R03只在T10交付core/hash机制，provider实际startup/codec guard属于T14–T17；T18仍须接线publisher/域映射/真实binding证明及独立active budget，T19负责预算wire。没有把worker完成或fake协议当全部W项/独立跨层/平台/live通过。

G03必须在所有accepted commits合流且本集中docs提交后执行任务包校验、全仓go test -count=1 ./...及go vet ./...，实际日志与状态由该SHA之后的外部result记录。原14类opt-in/平台/辅助skip不能计作native/live通过；若出现新增required skip或失败，重开owner并保留失败gate。未push、tag、发布或付费live。

R015 / G03-F01：首次合流372a465全仓3141pass/14原允许skip，但旧S9运行后Merge触发明确拒绝，vet未执行。T12 attempt3将S9迁移到既有运行前team.Option和原Stream，全部三成员业务断言保持；fake leader仅增加真实HTTP MCP能力。原Merge无绑定拒绝回归不变，未伪造证明或提前实现T18。C03独立逐块复核原断言与固定新SHA重放；失败证据保留external G03-attempt-1/T12-attempt-2。当前文件不代替新合流SHA的完整第二次G03门禁。
