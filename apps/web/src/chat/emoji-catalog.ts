// Loaded only through dynamic import() (see loadEmojiCatalog) so the full
// Unicode Emoji 17.0 data never lands in the main bundle.
import { EMOJI_GROUPS, EMOJI_VARIANTS } from "./emoji-data.generated";

export { EMOJI_GROUPS };

const allowed = new Set<string>(EMOJI_VARIANTS.split(" "));
for (const group of EMOJI_GROUPS) for (const [emoji] of group.entries) allowed.add(emoji);
const encoder = new TextEncoder();

/**
 * High-frequency Chinese names, synonyms, full pinyin, and pinyin initials for
 * common reaction emojis. Stays inside the lazy emoji-catalog chunk.
 */
const EMOJI_ZH_KEYWORDS: Readonly<Record<string, readonly [string, string]>> = {
  // 笑脸与情感
  "😀": ["咧嘴笑", "笑 开心 高兴 微笑 xiao kaixin gaoxing weixiao x kx gx wx"],
  "😃": ["大笑", "笑 开心 高兴 daxiao xiao kaixin dx x kx"],
  "😄": ["眉开眼笑", "笑 开心 哈哈 xiao kaixin haha x kx hh"],
  "😁": ["露齿笑", "嘻嘻 笑 开心 xixi xiao kaixin xx x kx"],
  "😆": ["眯眼大笑", "哈哈 笑 爆笑 haha xiao baoxiao hh x bx"],
  "😅": ["苦笑", "流汗 尴尬 汗 笑 kuxiao liuhan ganga han xiao kx lh gg"],
  "🤣": ["笑翻", "爆笑 笑死 哈哈 笑 xiaofan baoxiao xiaosi haha xiao xf bx xs"],
  "😂": ["笑哭", "破涕为笑 笑死 哈哈 笑 哭 xiaoku xiaosi haha xiao ku xk xs"],
  "🙂": ["微笑", "笑 礼貌 weixiao xiao wx"],
  "🙃": ["倒脸微笑", "无语 呵呵 笑 wuyu hehe xiao wy hh"],
  "😉": ["眨眼", "挤眼 暗示 懂的都懂 zhayan jiyan zy"],
  "😊": ["羞涩微笑", "开心 微笑 脸红 笑 kaixin weixiao lianhong kx wx lh"],
  "😇": ["天使微笑", "天使 善良 乖 tianshi shanliang ts"],
  "🥰": ["喜笑颜开", "喜欢 爱心 甜蜜 爱 xihuan aixin tianmi ai xh ax"],
  "😍": ["花痴", "喜欢 爱心 崇拜 爱 huachi xihuan aixin chongbai hc xh ax"],
  "🤩": ["星星眼", "崇拜 厉害 惊艳 哇 xingxingyan chongbai lihai xxy cb lh"],
  "😘": ["飞吻", "亲亲 么么哒 爱心 feiwen qinqin aixin fw qq"],
  "😋": ["馋嘴", "好吃 美味 吐舌 chanzui haochi meiwei cz hc"],
  "😜": ["吐舌眨眼", "调皮 搞怪 鬼脸 tiaopi gaoguai guilian tp gg"],
  "🤪": ["滑稽", "搞怪 疯了 逗比 huaji gaoguai doubi hj gg"],
  "🤗": ["抱抱", "拥抱 温暖 欢迎 baobao yongbao wennuan bb yb"],
  "🤭": ["偷笑", "捂嘴笑 害羞 笑 touxiao wuzuixiao haixiu tx"],
  "🤫": ["嘘", "安静 保密 别说话 xu anjing baomi"],
  "🤔": ["思考", "想想 琢磨 疑问 问号 sikao xiangxiang zuomo yiwen sk xx"],
  "🫡": ["敬礼", "收到 致敬 遵命 jingli shoudao zhijing jl sd"],
  "🤨": ["挑眉", "怀疑 疑惑 质疑 tiaomei huaiyi yihuo tm hy"],
  "😐": ["面无表情", "无语 沉默 淡定 wuyu chenmo danding wy cm"],
  "😑": ["无语", "无表情 沉默 发呆 wuyu chenmo fadai wy cm"],
  "😶": ["沉默", "无言 闭嘴 不说话 chenmo wuyan bizui cm"],
  "😏": ["得意", "坏笑 哼哼 懂的 deyi huaixiao dy hx"],
  "😒": ["不屑", "嫌弃 白眼 无语 buxie xianqi baiyan bx xq"],
  "🙄": ["翻白眼", "无语 白眼 算了 fanbaiyan wuyu baiyan fby wy"],
  "😬": ["尴尬", "呲牙 咬牙 社死 ganga ciya shesi gg"],
  "😌": ["松一口气", "安心 放心 释然 anxin fangxin shiran ax fx"],
  "😔": ["沉思", "难过 失落 叹气 chensi nanguo shiluo cs ng"],
  "😪": ["困", "犯困 睡觉 累 kun fankun shuijiao lei k fk sj"],
  "😴": ["睡觉", "晚安 困 打呼噜 shuijiao wanan kun sj wa"],
  "😷": ["戴口罩", "生病 感冒 口罩 shengbing ganmao kouzhao sb gm kz"],
  "🤒": ["发烧", "生病 体温 感冒 fashao shengbing ganmao fs sb"],
  "🤕": ["受伤", "绷带 疼 挂彩 shoushang bengdai teng ss"],
  "🤢": ["恶心", "想吐 难受 不舒服 exin xiangtu nanshou ex"],
  "🤮": ["呕吐", "吐了 恶心 outu tule exin ot"],
  "🥵": ["热晕", "好热 发烫 炎热 reyun haore fatang ry"],
  "🥶": ["冷死", "好冷 冻僵 冰冷 lengsi haoleng dongjiang ls"],
  "🤯": ["头炸了", "震撼 震惊 破防 脑洞 zhenhan zhenjing naodong zh zj"],
  "🥳": ["派对庆祝", "庆祝 生日 开心 撒花 qingzhu shengri paidui sahua qz sr pd"],
  "😎": ["酷", "墨镜 帅气 牛 稳 ku mojing shuaiqi niu wen k mj sq"],
  "🤓": ["书呆子", "眼镜 学霸 极客 认真 shudaizi yanjing xueba jike sdz xb"],
  "🧐": ["仔细观察", "观察 审查 研究 放大镜 guancha shencha yanjiu gc sc"],
  "😕": ["困惑", "迷茫 纠结 不解 kunhuo mimang jiujie kh mm"],
  "😟": ["担心", "忧虑 焦虑 发愁 danxin youlv jiaolv dx jl"],
  "😮": ["惊讶", "哇 吃惊 震惊 jingya chijing zhenjing jy cj"],
  "😲": ["震惊", "惊呆 哇塞 天啊 zhenjing jingdai tiana zj jd"],
  "😳": ["脸红", "害羞 震惊 尴尬 lianhong haixiu ganga lh hx"],
  "🥺": ["恳求", "求求 委屈 可怜 拜托 kenqiu qiuqiu weiqu baituo kq qq wq"],
  "🥹": ["感动", "忍泪 泪目 感动哭 gandong leimu renlei gd lm"],
  "😢": ["流泪", "哭 伤心 难过 liulei ku shangxin nanguo ll k sx ng"],
  "😭": ["大哭", "哭 痛哭 泪奔 伤心 难过 daku ku tongku leiben shangxin nanguo dk k lb"],
  "😱": ["惊恐", "吓死 尖叫 害怕 天哪 jingkong xiasi jianjiao haipa jk xs hp"],
  "😞": ["失望", "沮丧 难过 叹气 shiwang jusang nanguo sw js"],
  "😫": ["累坏", "崩溃 疲惫 心累 抓狂 leihuai bengkui pibei xinlei lh bk pb"],
  "🥱": ["打哈欠", "困 无聊 累 dahaqian kun wuliao dhq"],
  "😤": ["傲娇", "哼 得意 生气 冲啊 aojiao heng shengqi chonga aj sq"],
  "😡": ["愤怒", "生气 发火 怒 气死 fennu shengqi fahuo nu fn sq"],
  "😠": ["生气", "不爽 恼火 怒 shengqi bushuang naohuo sq"],
  "🤬": ["爆粗口", "气炸 骂人 怒 qizha maren nu qz"],
  "💀": ["骷髅", "笑死 无语 寄了 死亡 kulou xiaosi jile kl xs"],
  "💩": ["便便", "屎 搞怪 糟糕 bianbian shi zaogao bb"],
  "🤡": ["小丑", "尴尬 搞笑 滑稽 xiaochou gaoxiao xc"],
  "👻": ["幽灵", "鬼 调皮 吓人 youling gui yl"],
  "🤖": ["机器人", "AI Agent 智能 机械 jiqiren zhineng jqr ai"],
  "🙈": ["非礼勿视", "害羞 捂眼 没眼看 haixiu wuyan meiyankan hx wy"],
  "🙉": ["非礼勿听", "捂耳 不听 wuer buting we"],
  "🙊": ["非礼勿言", "捂嘴 说漏嘴 wuzui wz"],
  // 爱心与符号情绪
  "❤️": ["红心", "爱心 喜欢 爱 心 比心 aixin hongxin xihuan ai xin bixin ax hx xh"],
  "🧡": ["橙心", "爱心 心 温暖 aixin chengxin ax"],
  "💛": ["黄心", "爱心 心 友情 aixin huangxin ax"],
  "💚": ["绿心", "爱心 心 环保 aixin lvxin ax"],
  "💙": ["蓝心", "爱心 心 信任 aixin lanxin ax"],
  "💜": ["紫心", "爱心 心 aixin zixin ax"],
  "🖤": ["黑心", "爱心 心 暗黑 aixin heixin ax"],
  "🤍": ["白心", "爱心 心 纯洁 aixin baixin ax"],
  "💔": ["心碎", "伤心 难过 失恋 xinsui shangxin nanguo xs sx"],
  "💖": ["闪亮爱心", "爱心 喜欢 闪亮 aixin xihuan shanliang ax"],
  "💘": ["一箭穿心", "爱心 心动 恋爱 aixin xindong lianai ax xd"],
  "💝": ["礼物爱心", "爱心 礼物 aixin liwu ax lw"],
  "💕": ["两颗心", "爱心 甜蜜 喜欢 aixin tianmi xihuan ax"],
  "💯": ["满分", "100 完美 厉害 同意 manfen wanmei lihai tongyi mf wm 100"],
  "💥": ["爆炸", "碰撞 震撼 炸裂 baozha pengzhuang zhalie bz"],
  "💫": ["眩晕", "星星 闪亮 晕 xuanyun xingxing xy"],
  "💦": ["汗滴", "流汗 努力 汗 handi liuhan nuli hd lh"],
  "💨": ["冲刺", "快跑 溜了 疾风 chongci kuaipao liule cc"],
  "💬": ["对话气泡", "聊天 评论 讨论 沟通 liaotian pinglun taolun goutong lt pl tl"],
  "💭": ["思考气泡", "想法 梦想 思考 xiangfa mengxiang sikao xf sk"],
  "💤": ["打盹", "睡觉 困 休息 shuijiao kun xiuxi sj"],
  // 人物与手势
  "👋": ["挥手", "你好 再见 打招呼 拜拜 huishou nihao zaijian dazhaohu baibai hs nh zj bb"],
  "✋": ["举手", "停 举手 击掌 jushou ting jizhang js"],
  "👌": ["好的", "OK 没问题 可以 收到 ok haode meiwenti keyi shoudao hd sd"],
  "✌️": ["胜利", "耶 剪刀手 和平 成功 shengli ye jiandaoshou chenggong sl"],
  "🤞": ["祈祷好运", "好运 祝顺利 haoyun shunli hy"],
  "🤟": ["爱你手势", "爱你 摇滚 aini yaogun an"],
  "🤙": ["打电话", "666 厉害 联系 好的 dadianhua 666 lihai lianxi ddh"],
  "👈": ["向左指", "左边 左 xiangzuo zuobian zb"],
  "👉": ["向右指", "右边 右 看这里 xiangyou youbian yb"],
  "👆": ["向上指", "上面 上 楼上 xiangshang shangmian loushang ls"],
  "👇": ["向下指", "下面 下 楼下 xiangxia xiamian louxia lx"],
  "👍": ["点赞", "赞 好的 厉害 同意 支持 牛 收到 zan dianzan haode lihai tongyi zhichi niu shoudao z dz hd ty zc"],
  "👎": ["点踩", "踩 反对 不好 不赞成 cai diancai fandui buhao c dc fd"],
  "✊": ["握拳", "加油 坚持 团结 woquan jiayou jianchi wq jy"],
  "👊": ["碰拳", "击拳 加油 兄弟 pengquan jiquan jiayou pq jy"],
  "👏": ["鼓掌", "拍手 厉害 恭喜 赞叹 guzhang paishou lihai gongxi gz ps gx"],
  "🙌": ["举双手庆祝", "欢呼 太棒了 万岁 庆祝 huanhu taibangle wansui qingzhu hh qz"],
  "🫶": ["比心", "爱心 喜欢 感谢 bixin aixin xihuan ganxie bx ax gx"],
  "🤝": ["握手", "合作 成交 达成一致 同意 woshou hezuo chengjiao tongyi ws hz cj"],
  "🙏": ["双手合十", "感谢 谢谢 拜托 祈祷 辛苦了 ganxie xiexie baituo qidao xinkule gx xx bt"],
  "💪": ["加油", "力量 强壮 努力 冲鸭 jiayou liliang nuli qiangzhuang jy ll nl"],
  "👀": ["围观", "眼睛 看 关注 看看 盯 yanjing weiguan kan guanzhu kankan yj wg gz"],
  "🧠": ["大脑", "聪明 思考 智慧 脑子 danao congming sikao zhihui dn cm"],
  // 动物与自然
  "🐶": ["小狗", "狗 旺财 汪 宠物 gou xiaogou wangcai chongwu g xg"],
  "🐱": ["小猫", "猫 喵 宠物 mao xiaomao miao chongwu m xm"],
  "🦊": ["狐狸", "聪明 狡猾 huli hl"],
  "🐻": ["小熊", "熊 可爱 xiong xiaoxiong x"],
  "🐼": ["熊猫", "国宝 可爱 xiongmao guobao xm"],
  "🐯": ["老虎", "虎 霸气 laohu hu lh"],
  "🦁": ["狮子", "狮 霸气 shizi sz"],
  "🐷": ["小猪", "猪 佩奇 zhu xiaozhu z"],
  "🐸": ["青蛙", "蛙 瓜 qingwa wa qw"],
  "🐵": ["猴子", "猴 调皮 houzi hou hz"],
  "🐧": ["企鹅", "鹅 可爱 qie qe"],
  "🦅": ["老鹰", "鹰 飞翔 laoying ly"],
  "🦄": ["独角兽", "梦幻 稀有 dujiaoshou djs"],
  "🐝": ["蜜蜂", "勤劳 忙 mifeng qinlao mf"],
  "🦋": ["蝴蝶", "美丽 蜕变 hudie hd"],
  "🐞": ["瓢虫", "虫子 bug 缺陷 piaochong chongzi bug pc"],
  "🐛": ["毛毛虫", "虫子 bug 缺陷 chongzi bug cz"],
  "🐢": ["乌龟", "慢 稳 wugui man wg"],
  "🐳": ["鲸鱼", "海 蓝鲸 Docker jingyu hai jy"],
  "🐙": ["章鱼", "触手 多线程 zhangyu zy"],
  "🌸": ["樱花", "花 春天 好看 yinghua hua chuntian yh h"],
  "🌹": ["玫瑰", "花 浪漫 感谢 meigui hua langman mg h"],
  "🌻": ["向日葵", "花 阳光 积极 xiangrikui yangguang xrk"],
  "🌱": ["嫩芽", "成长 发芽 新生 植物 nenya chengzhang faya ny cz"],
  "🍀": ["四叶草", "幸运 好运 幸运草 siyecao xingyun haoyun syc xy"],
  "🔥": ["火焰", "火 热 爆 厉害 给力 热门 huo huoyan re bao lihai geili h hy"],
  "💧": ["水滴", "水 汗 眼泪 shuidi shui sd"],
  "🌊": ["海浪", "浪 海洋 冲浪 hailang lang haiyang hl"],
  "☀️": ["太阳", "晴天 阳光 早上好 taiyang qingtian yangguang zaoshanghao ty"],
  "🌙": ["月亮", "晚安 夜晚 yueliang wanan yewan yl wa"],
  "⭐": ["星星", "收藏 优秀 评分 推荐 xingxing shoucang youxiu pingfen xx sc"],
  "🌟": ["闪耀星星", "星星 闪亮 精彩 亮点 xingxing shanliang jingcai liangdian xx ld"],
  "🌈": ["彩虹", "希望 美好 多彩 caihong xiwang meihao ch"],
  "⚡": ["闪电", "快 能量 极速 高效 shandian kuai nengliang jisu sd"],
  "❄️": ["雪花", "冷 冬天 冰雪 xuehua leng dongtian xh"],
  // 食物与饮料
  "🍎": ["红苹果", "苹果 水果 pingguo shuiguo pg"],
  "🍉": ["西瓜", "吃瓜 夏天 水果 xigua chigua xiatian xg cg"],
  "🍓": ["草莓", "水果 甜 caomei shuiguo cm"],
  "🍔": ["汉堡", "快餐 吃饭 饿了 hanbao kuaican chifan hb"],
  "🍕": ["披萨", "聚餐 美食 pisa jucan meishi ps"],
  "🍜": ["拉面", "面条 吃饭 宵夜 lamian miantiao chifan lm"],
  "🍣": ["寿司", "日料 美食 shousi riliao ss"],
  "🥟": ["饺子", "过年 美食 jiaozi guonian jz"],
  "🍰": ["蛋糕", "甜点 庆祝 下午茶 dangao tiandian dg"],
  "🎂": ["生日蛋糕", "生日 庆祝 快乐 shengri dangao qingzhu sr dg"],
  "🍩": ["甜甜圈", "甜点 零食 tiantianquan ttq"],
  "☕": ["咖啡", "提神 休息 摸鱼 下午茶 早上好 kafei tishen xiuxi moyu kf"],
  "🍵": ["热茶", "喝茶 休息 养生 recha hecha cha rc hc"],
  "🍺": ["啤酒", "干杯 聚会 庆祝 pijiu ganbei juhui pj gb"],
  "🍻": ["干杯", "啤酒 庆祝 聚餐 开心 ganbei pijiu qingzhu gb"],
  "🥂": ["碰杯", "香槟 庆祝 干杯 pengbei xiangbin qingzhu ganbei pb gb"],
  "🧋": ["奶茶", "饮料 下午茶 快乐水 naicha yinliao nc"],
  // 旅行与地点
  "🏠": ["房子", "家 居家 远程办公 fangzi jia yuancheng fz"],
  "🏢": ["办公楼", "公司 上班 办公室 bangonglou gongsi shangban bgl gs"],
  "✈️": ["飞机", "出差 旅行 航班 起飞 feiji chuchai lvxing qifei fj"],
  "🚀": ["火箭", "发布 上线 起飞 冲 加速 推进 huojian fabu shangxian qifei jiasu hj fb sx qf"],
  "⏰": ["闹钟", "提醒 时间 截止 定时 naozhong tixing shijian jiezhi nz tx"],
  "⌛": ["沙漏", "等待 时间 倒计时 shalou dengdai daojishi sl"],
  // 活动
  "🎉": ["庆祝", "拉花 恭喜 撒花 派对 发布 快乐 qingzhu lahua gongxi sahua paidui fabu qz gx sh"],
  "🎊": ["彩球", "庆祝 恭喜 撒花 派对 caiqiu qingzhu gongxi sahua cq qz"],
  "✨": ["闪闪发光", "亮点 神奇 精致 星星 新特性 魔法 liangdian shenqi shanliang xingxing mofa ld sq"],
  "🎈": ["气球", "庆祝 生日 派对 qiqiu qingzhu shengri qq"],
  "🎁": ["礼物", "惊喜 福利 赠送 奖励 liwu jingxi fuli jiangli lw"],
  "🏆": ["奖杯", "冠军 胜利 第一 荣誉 成就 jiangbei guanjun shengli rongyu jb gj"],
  "🥇": ["金牌", "第一 冠军 优秀 jinpai diyi guanjun jp"],
  "🎯": ["命中目标", "目标 精准 靶心 重点 达成 mubiao jingzhun baxin zhongdian mb jz"],
  "🎮": ["游戏手柄", "游戏 娱乐 开黑 youxi shoubing kaihei yx"],
  "🎨": ["调色盘", "设计 美术 创意 视觉 sheji meishu chuangyi sj"],
  "🎵": ["音符", "音乐 唱歌 节奏 yinfu yinyue yf yy"],
  "🎶": ["多个音符", "音乐 旋律 节奏 yinyue xuanlv yy"],
  // 物品
  "🔔": ["铃铛", "通知 提醒 关注 订阅 lingdang tongzhi tixing ld tz tx"],
  "📣": ["扩音器", "公告 广播 宣传 通知 gonggao guangbo tongzhi gg gb"],
  "📢": ["喇叭", "公告 通知 播报 laba gonggao tongzhi lb gg"],
  "📱": ["手机", "移动 电话 联系 shouji yidong dianhua sj"],
  "💻": ["笔记本电脑", "电脑 写代码 工作 开发 diannao daima gongzuo kaifa dn dm kf"],
  "⌨️": ["键盘", "打字 编程 快捷 jianpan dazi biancheng jp"],
  "🔍": ["放大镜", "搜索 查找 排查 调研 sousuo chazhao paicha diaoyan ss cz pc"],
  "💡": ["灯泡", "灵感 想法 提示 主意 建议 linggan xiangfa tishi zhuyi jianyi lg xf ts"],
  "📖": ["打开的书", "文档 阅读 学习 资料 wendang yuedu xuexi ziliao wd yd xx"],
  "📚": ["书堆", "学习 资料 文档 知识 xuexi ziliao wendang zhishi xx wd"],
  "💰": ["钱袋", "发财 奖金 预算 赚钱 qiandai facai jiangjin yusuan qd fc"],
  "✉️": ["信封", "邮件 联系 消息 xinfeng youjian xiaoxi xf yj"],
  "📦": ["包裹", "发版 打包 快递 交付 baoguo faban dabao jiaofu bg fb"],
  "📝": ["备忘录", "记录 笔记 文档 写字 纪要 jilu biji wendang jiyao jl bj"],
  "📅": ["日历", "日程 排期 日期 会议 rili richeng paiqi huiyi rl rc pq"],
  "📈": ["上升趋势", "增长 上涨 业绩 数据 提升 zengzhang shangzhang shuju tisheng zz sj"],
  "📉": ["下降趋势", "下跌 减少 降低 xiajiang jianshao xj"],
  "📊": ["柱状图", "统计 报表 数据 分析 tongji baobiao shuju fenxi tj bb sj"],
  "📌": ["图钉", "置顶 固定 标记 重点 zhiding guding biaoji zhongdian zd gd"],
  "📎": ["回形针", "附件 链接 文件 fujian lianjie wenjian fj"],
  "🔒": ["锁", "安全 加密 权限 私密 anquan jiami quanxian simi aq jm"],
  "🔑": ["钥匙", "关键 密钥 权限 解决 yaoshi guanjian miyao quanxian ys gj"],
  "🔧": ["扳手", "修复 调试 工具 维护 xiufu tiaoshi gongju weihu xf ts gj"],
  "⚙️": ["齿轮", "设置 配置 系统 引擎 shezhi peizhi xitong sz pz"],
  "🛠️": ["锤子扳手", "维修 构建 开发 工具 排查 weixiu goujian kaifa gongju wx gj"],
  // 符号
  "⚠️": ["警告", "注意 提醒 风险 告警 jinggao zhuyi tixing fengxian gaojing jg zy fx"],
  "⛔": ["禁止进入", "禁止 阻塞 拒绝 拦截 jinzhi zusai jujue lanjie jz"],
  "🚫": ["禁止", "不行 禁用 取消 拒绝 jinzhi buxing quxiao jujue jz"],
  "✅": ["勾选", "完成 对勾 通过 确认 收到 搞定 好的 gouxuan wancheng duigou tongguo queren shoudao gaoding gx wc tg qr"],
  "☑️": ["勾选框", "完成 确认 选中 gouxuan wancheng queren gx wc"],
  "✔️": ["对勾", "勾选 正确 确认 完成 duigou gouxuan zhengque wancheng dg gx"],
  "❌": ["叉号", "错误 失败 取消 驳回 不行 chahao cuowu shibai quxiao bohui cw sb qx"],
  "❓": ["问号", "疑问 问题 不懂 困惑 wenhao yiwen wenti wh yw wt"],
  "❗": ["感叹号", "重要 紧急 注意 提醒 gantanhao zhongyao jinji zhuyi gth zy jj"],
  "🔄": ["刷新", "同步 重试 循环 更新 shuaxin tongbu chongshi gengxin sx tb cs"],
  "🆕": ["新品", "新 最新 新增 xin zuixin xinzeng x"],
  "🆗": ["OK", "好的 收到 可以 没问题 ok haode shoudao keyi"],
  "🆘": ["求助", "紧急 救命 支援 帮忙 qiuzhu jinji jiuming zhiyuan bangmang qz jj sos"],
  "🚩": ["红旗", "标记 里程碑 风险 提醒 hongqi biaoji lichengbei hq bj"],
  "🏁": ["终点旗", "完成 冲线 结束 达标 zhongdian wancheng jieshu zd wc"],
};

const GROUP_PINYIN: Readonly<Record<string, string>> = {
  smileys: "xiaolian qinggan biaoqing xl qg bq",
  people: "renwu shenti shoushi rw st ss",
  nature: "dongwu ziran zhiwu tianqi dw zr",
  food: "shiwu yinliao meishi sw yl ms",
  travel: "lvxing didian jiaotong lx dd jt",
  activities: "huodong yundong yule hd yd",
  objects: "wupin gongju shebei wp gj",
  symbols: "fuhao biaozhi fh bz",
  flags: "qizhi guoqi qz gq",
};

const EMOJI_LABEL_MAP = new Map<string, string>();
for (const group of EMOJI_GROUPS) {
  for (const [emoji, englishName] of group.entries) {
    const zh = EMOJI_ZH_KEYWORDS[emoji];
    EMOJI_LABEL_MAP.set(emoji, zh ? zh[0] : englishName);
  }
}

/**
 * Contract rule for reaction emoji: exactly one fully-qualified Unicode
 * Emoji 17.0 sequence from the pinned data, at most 64 UTF-8 bytes.
 */
export function isReactionEmoji(value: unknown): value is string {
  return (
    typeof value === "string" &&
    encoder.encode(value).length <= 64 &&
    allowed.has(value)
  );
}

export const allowedEmojiCount = () => allowed.size;

/** Readable display/accessible label for an emoji (Chinese when curated, else CLDR English). */
export function emojiLabel(emoji: string): string {
  return EMOJI_LABEL_MAP.get(emoji) ?? "";
}

const SEARCH_GROUPS = EMOJI_GROUPS.map((group) => ({
  labelSearch: `${group.label.toLocaleLowerCase()} ${GROUP_PINYIN[group.id] ?? ""}`,
  entries: group.entries.map(([emoji, name]) => {
    const zh = EMOJI_ZH_KEYWORDS[emoji];
    const searchText = zh
      ? `${zh[0]} ${zh[1]} ${name.toLocaleLowerCase()}`
      : name.toLocaleLowerCase();
    return [emoji, searchText] as const;
  }),
}));

/** Name/keyword/category search over picker entries (Chinese, pinyin, English CLDR, group labels). */
export function searchEmojis(query: string, limit = 160): string[] {
  const needle = query.trim().toLocaleLowerCase();
  if (!needle) return [];
  const primary: string[] = [];
  const secondary: string[] = [];
  const seen = new Set<string>();
  for (const group of SEARCH_GROUPS) {
    const groupMatch = group.labelSearch.includes(needle);
    for (const [emoji, searchText] of group.entries) {
      if (seen.has(emoji)) continue;
      if (emoji === needle || searchText.includes(needle)) {
        seen.add(emoji);
        primary.push(emoji);
        if (primary.length >= limit) return primary;
      } else if (groupMatch && primary.length + secondary.length < limit) {
        seen.add(emoji);
        secondary.push(emoji);
      }
    }
  }
  return primary.length >= limit
    ? primary.slice(0, limit)
    : [...primary, ...secondary.slice(0, limit - primary.length)];
}
