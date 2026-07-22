# 发布影响面验证 (verify)

在每次发布 / 升级 / 改配置的前后各拍一次整机快照,自动对账出"这次改动到底动了什么"。
回答运维每天最提心吊胆的问题:我这次上线,除了预期的改动,有没有悄悄动了别的?

## 用法

发布前打基线:

    deltascope verify start -name deploy-2024w30

执行你的发布 (升级包 / 改配置 / 重启服务 / 跑 ansible ...)。

发布后出影响面报告:

    deltascope verify report -name deploy-2024w30

## 接入 CI / PR

生成 Markdown,贴进 PR 评论或 Slack:

    deltascope verify report -name $CI_COMMIT_SHA -format md -title "本次部署影响面" > impact.md

report 在检出变更时返回退出码 3 —— CI 里可据此要求人工确认:

    deltascope verify start -name $CI_COMMIT_SHA
    ./deploy.sh
    if ! deltascope verify report -name $CI_COMMIT_SHA -format md > impact.md; then
        gh pr comment --body-file impact.md   # 有变更,贴报告并卡住等确认
    fi

## 与 statediff 的区别

- statediff 面向"日常巡检":定时快照,看今天 vs 昨天自然漂移了什么。
- verify 面向"一次发布":显式打基线,精确框定"这次操作"的影响面,基线按名字持久保存。
