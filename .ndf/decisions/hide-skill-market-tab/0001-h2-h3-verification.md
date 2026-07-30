# hide-skill-market-tab H2/H3 Verification

Date: 2026-07-30

## Scope

Hide the user frontend sidebar tab labeled `技能市场` for both parent accounts and child accounts. This change intentionally keeps the existing marketplace routes and backend parent-only guards unchanged.

## Commits

- `4de02a4` `test(qa): reproduce hidden skill marketplace tab`
- `32b1ae8` `fix(nav): hide skill marketplace tab`
- `58b5ee7` merge commit on `numind-web-v3/develop`

## Evidence

- RED unit test failed before the fix: parent sidebar text still contained `技能市场`.
- Targeted unit test after the fix passed: `npx vitest run src/components/layout/__tests__/AppSidebar.spec.ts`.
- Local Playwright parent-account mock after the fix returned sidebar labels `工作区, 运行记录, 客户管理, 知识库, 选题库, 配置中心, 设置`, with `marketplaceCount=0` and no JS errors.
- `npm run lint` passed with existing warnings only.
- `npm run type-check` passed.
- `ndf-done` merged and pushed `numind-web-v3/develop` at `58b5ee7`.
- Dev deployment succeeded for image `ccr.ccs.tencentyun.com/youshunumind/numind-web-v3:develop-58b5ee7`, registry digest `sha256:346a1ab712c348cdcc7352d3ac1661ba9c9d1d71fd01bae14f3747cc4344ea77`.
- Public Dev health check returned `healthy`.
- Real Dev Playwright login using the configured E2E parent account returned `marketplaceCount=0`, sidebar labels `工作区, 运行记录, 客户管理, 知识库, 选题库, 配置中心, 设置`, and no page errors.

## Residual Risk

Direct `/marketplace` URLs remain routable for parent accounts because the requested change was tab visibility only. Production deployment still requires the normal tag/release authorization.
