# Admin Feed OPML Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let admins import and export global `external_rss` feed sources through OPML from the existing subscription source management panel.

**Architecture:** Reuse the feed module's existing OPML structs and global import handler. Add a protected global export route beside the existing import route, expose both URLs through the frontend API helper, add store methods for multipart upload and blob download, then add compact controls to `SettingFeedSourcePanel.vue`.

**Tech Stack:** Go, Gin, GORM, XML OPML, Vue 3, Pinia, Vite, Vitest.

---

### Task 1: Backend Global OPML Export

**Files:**
- Modify: `Atoman-Backend/internal/modules/feed/legacy_compat_test.go`
- Modify: `Atoman-Backend/internal/modules/feed/legacy_compat.go`
- Modify: `Atoman-Backend/internal/modules/feed/http.go`

- [ ] **Step 1: Add backend export tests**

Append these tests near the existing `ImportGlobalOPML` tests in `Atoman-Backend/internal/modules/feed/legacy_compat_test.go`:

```go
func TestExportGlobalOPMLExportsExternalRSSOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := newFeedHandlerTestDB(t)

	external := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "https://example.com/feed.xml",
		Title:      "Example Feed",
		Hash:       "export-external-" + uuid.NewString(),
	}
	if err := db.Create(&external).Error; err != nil {
		t.Fatalf("create external source: %v", err)
	}
	internal := model.FeedSource{
		SourceType: "internal_user",
		RssURL:     "https://example.com/internal.xml",
		Title:      "Internal Feed",
		Hash:       "export-internal-" + uuid.NewString(),
	}
	if err := db.Create(&internal).Error; err != nil {
		t.Fatalf("create internal source: %v", err)
	}
	blank := model.FeedSource{
		SourceType: "external_rss",
		RssURL:     "",
		Title:      "Blank Feed",
		Hash:       "export-blank-" + uuid.NewString(),
	}
	if err := db.Create(&blank).Error; err != nil {
		t.Fatalf("create blank source: %v", err)
	}

	router := gin.New()
	feed := router.Group("/api/v1/feed")
	feed.GET("/sources/opml/export", ExportGlobalOPML(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "application/x-opml+xml") {
		t.Fatalf("expected OPML content type, got %q", got)
	}
	if got := w.Header().Get("Content-Disposition"); !strings.Contains(got, "atoman-feed-sources.opml") {
		t.Fatalf("expected OPML attachment filename, got %q", got)
	}
	var parsed OPML
	if err := xml.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode exported OPML: %v", err)
	}
	if parsed.Head.Title != "Atoman Feed Sources" {
		t.Fatalf("unexpected OPML title %q", parsed.Head.Title)
	}
	if len(parsed.Body.Outlines) != 1 {
		t.Fatalf("expected one exported source, got %d: %#v", len(parsed.Body.Outlines), parsed.Body.Outlines)
	}
	outline := parsed.Body.Outlines[0]
	if outline.Text != "Example Feed" || outline.Title != "Example Feed" || outline.Type != "rss" || outline.XMLURL != "https://example.com/feed.xml" {
		t.Fatalf("unexpected exported outline: %#v", outline)
	}
}

func TestExportGlobalOPMLRequiresAdminThroughRealRoute(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("JWT_SECRET", "test-secret")
	db := newFeedHandlerTestDB(t)
	user := seedFeedTestUser(t, db)
	admin := seedFeedAdminUser(t, db)

	router := gin.New()
	RegisterRoutes(router.Group("/api/v1/feed"), NewService(db))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, user))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/feed/sources/opml/export", nil)
	req.Header.Set("Authorization", "Bearer "+signedFeedTokenForTest(t, admin))
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin, got %d: %s", w.Code, w.Body.String())
	}
}
```

Also add `encoding/xml` to the test imports if it is not already present.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/feed -run 'TestExportGlobalOPML' -count=1
```

Expected: fails because `ExportGlobalOPML` is undefined or the route is not registered.

- [ ] **Step 3: Implement `ExportGlobalOPML`**

Add this handler after `ImportGlobalOPML` in `Atoman-Backend/internal/modules/feed/legacy_compat.go`:

```go
// ExportGlobalOPML godoc
// @Summary 导出全局 OPML 订阅源
// @Description 管理员导出全站 external_rss 订阅源为 OPML 文件，不包含用户订阅关系。
// @Tags feed
// @Produce application/x-opml+xml
// @Success 200 {string} string "OPML XML"
// @Failure 401 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Failure 500 {object} ErrorResponse
// @Security BearerAuth
// @Security CookieAuth
// @Router /api/v1/feed/sources/opml/export [get]
func ExportGlobalOPML(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var sources []model.FeedSource
		if err := db.
			Where("source_type = ? AND COALESCE(rss_url, '') <> ?", "external_rss", "").
			Order("title ASC, created_at ASC").
			Find(&sources).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch feed sources"})
			return
		}

		opml := OPML{
			Version: "2.0",
			Head: OPMLHead{
				Title: "Atoman Feed Sources",
			},
		}

		for _, source := range sources {
			title := strings.TrimSpace(source.Title)
			if title == "" {
				title = strings.TrimSpace(source.RssURL)
			}
			opml.Body.Outlines = append(opml.Body.Outlines, OPMLOutline{
				Text:   title,
				Title:  title,
				Type:   "rss",
				XMLURL: source.RssURL,
			})
		}

		output, err := xml.MarshalIndent(opml, "", "  ")
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate OPML"})
			return
		}

		c.Header("Content-Type", "application/x-opml+xml")
		c.Header("Content-Disposition", "attachment; filename=\"atoman-feed-sources.opml\"")
		c.Data(http.StatusOK, "application/x-opml+xml", output)
	}
}
```

- [ ] **Step 4: Register the export route**

In `Atoman-Backend/internal/modules/feed/http.go`, add this route beside the existing global OPML import route:

```go
protected.GET("/sources/opml/export", middleware.AdminMiddleware(service.db), ExportGlobalOPML(service.db))
```

- [ ] **Step 5: Run backend OPML tests**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/feed -run 'Test(ImportGlobalOPML|ExportGlobalOPML)' -count=1
```

Expected: all selected OPML tests pass.

### Task 2: Frontend API And Store Methods

**Files:**
- Modify: `Atoman-Frontend/src/composables/useApi.ts`
- Modify: `Atoman-Frontend/src/stores/adminFeedFulltext.ts`

- [ ] **Step 1: Add frontend store tests**

Extend `Atoman-Frontend/tests/unit/components/setting/SettingFeedSourcePanel.spec.ts` setup with mocked store methods:

```ts
const importGlobalOPML = vi.fn().mockResolvedValue({ imported: 1, reused: 2, failed: 0 })
const exportGlobalOPML = vi.fn().mockResolvedValue(new Blob(['opml'], { type: 'application/x-opml+xml' }))
```

Add both methods to `storeState`.

- [ ] **Step 2: Add API endpoints**

In `Atoman-Frontend/src/composables/useApi.ts`, add the OPML endpoints under `admin.feed`:

```ts
opmlImport: `${apiUrl}/feed/sources/opml/import`,
opmlExport: `${apiUrl}/feed/sources/opml/export`,
```

- [ ] **Step 3: Add store methods**

In `Atoman-Frontend/src/stores/adminFeedFulltext.ts`, add this response type near the existing interfaces:

```ts
export interface AdminFeedOPMLImportResult {
  message: string
  imported: number
  reused: number
  failed: number
}
```

Then add these methods inside the store:

```ts
async function importGlobalOPML(file: File, token: string | null): Promise<AdminFeedOPMLImportResult> {
  const formData = new FormData()
  formData.append('file', file)

  const response = await fetch(api.admin.feed.opmlImport, {
    method: 'POST',
    headers: buildHeaders(token),
    body: formData,
  })

  if (!response.ok) {
    throw new Error(await parseError(response, '导入 OPML 失败'))
  }

  return response.json()
}

async function exportGlobalOPML(token: string | null): Promise<Blob> {
  const response = await fetch(api.admin.feed.opmlExport, {
    headers: buildHeaders(token),
  })

  if (!response.ok) {
    throw new Error(await parseError(response, '导出 OPML 失败'))
  }

  return response.blob()
}
```

Return both methods from the store.

- [ ] **Step 4: Run a focused type check**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run type-check
```

Expected: TypeScript accepts the new API/store methods.

### Task 3: Admin Panel OPML Controls

**Files:**
- Modify: `Atoman-Frontend/src/components/setting/SettingFeedSourcePanel.vue`
- Modify: `Atoman-Frontend/tests/unit/components/setting/SettingFeedSourcePanel.spec.ts`

- [ ] **Step 1: Add component tests for OPML controls**

Add tests to `Atoman-Frontend/tests/unit/components/setting/SettingFeedSourcePanel.spec.ts`:

```ts
it('导入 OPML 后刷新订阅源列表并显示统计', async () => {
  const file = new File(['<opml version="2.0"><body /></opml>'], 'feeds.opml', { type: 'text/xml' })
  const wrapper = mount(SettingFeedSourcePanel, {
    props: { fullTextMode: 'per_source' },
    global: { stubs },
  })

  await flushPromises()
  fetchSources.mockClear()

  const input = wrapper.find('input[type="file"]')
  Object.defineProperty(input.element, 'files', {
    value: [file],
    configurable: true,
  })
  await input.trigger('change')
  await flushPromises()

  expect(importGlobalOPML).toHaveBeenCalledWith(file, 'admin-token')
  expect(fetchSources).toHaveBeenCalledWith('admin-token', { limit: 100 })
  expect(wrapper.text()).toContain('导入 1，复用 2，失败 0')
})

it('点击导出 OPML 时下载后端返回的文件', async () => {
  const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:opml')
  const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
  const click = vi.fn()
  const anchor = document.createElement('a')
  vi.spyOn(anchor, 'click').mockImplementation(click)
  const createElement = vi.spyOn(document, 'createElement').mockImplementation((tagName: string) => {
    if (tagName === 'a') return anchor
    return document.createElement(tagName)
  })

  const wrapper = mount(SettingFeedSourcePanel, {
    props: { fullTextMode: 'per_source' },
    global: { stubs },
  })

  await flushPromises()
  const exportButton = wrapper.findAll('button').find((button) => button.text() === '导出 OPML')
  expect(exportButton).toBeTruthy()
  await exportButton!.trigger('click')
  await flushPromises()

  expect(exportGlobalOPML).toHaveBeenCalledWith('admin-token')
  expect(createObjectURL).toHaveBeenCalled()
  expect(anchor.download).toBe('atoman-feed-sources.opml')
  expect(click).toHaveBeenCalled()
  expect(revokeObjectURL).toHaveBeenCalledWith('blob:opml')

  createElement.mockRestore()
  createObjectURL.mockRestore()
  revokeObjectURL.mockRestore()
})
```

If the recursive `document.createElement` fallback causes problems, store the original before spying and call the original in the fallback.

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit tests/unit/components/setting/SettingFeedSourcePanel.spec.ts
```

Expected: tests fail because the OPML controls and handlers do not exist yet.

- [ ] **Step 3: Add OPML UI controls**

In `Atoman-Frontend/src/components/setting/SettingFeedSourcePanel.vue`, add this action block after the header and before the editor:

```vue
    <div class="setting-feed-panel__opml">
      <input
        ref="opmlInput"
        class="setting-feed-panel__file-input"
        type="file"
        accept=".opml,.xml"
        @change="importOPML"
      />
      <ABtn
        size="sm"
        variant="secondary"
        :disabled="importingOPML"
        :loading="importingOPML"
        loading-text="导入中..."
        @click="openOPMLPicker"
      >
        导入 OPML
      </ABtn>
      <ABtn
        size="sm"
        variant="secondary"
        :disabled="exportingOPML"
        :loading="exportingOPML"
        loading-text="导出中..."
        @click="exportOPML"
      >
        导出 OPML
      </ABtn>
    </div>
```

Add refs in `<script setup>`:

```ts
const importingOPML = ref(false)
const exportingOPML = ref(false)
const opmlInput = ref<HTMLInputElement | null>(null)
```

Add handlers:

```ts
function openOPMLPicker() {
  opmlInput.value?.click()
}

async function importOPML(event: Event) {
  if (!authStore.token) return
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return

  importingOPML.value = true
  error.value = ''
  message.value = ''
  try {
    const result = await adminFeedFulltextStore.importGlobalOPML(file, authStore.token)
    message.value = `导入 ${result.imported || 0}，复用 ${result.reused || 0}，失败 ${result.failed || 0}`
    await refresh()
  } catch (err) {
    error.value = err instanceof Error ? err.message : '导入 OPML 失败'
  } finally {
    importingOPML.value = false
    input.value = ''
  }
}

async function exportOPML() {
  if (!authStore.token) return

  exportingOPML.value = true
  error.value = ''
  message.value = ''
  try {
    const blob = await adminFeedFulltextStore.exportGlobalOPML(authStore.token)
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = 'atoman-feed-sources.opml'
    link.click()
    URL.revokeObjectURL(url)
  } catch (err) {
    error.value = err instanceof Error ? err.message : '导出 OPML 失败'
  } finally {
    exportingOPML.value = false
  }
}
```

Add CSS:

```css
.setting-feed-panel__opml {
  display: flex;
  gap: 0.75rem;
  justify-content: flex-end;
  flex-wrap: wrap;
}

.setting-feed-panel__file-input {
  display: none;
}
```

In the mobile media query, include `.setting-feed-panel__opml` in the `justify-content: flex-start` rule.

- [ ] **Step 4: Run component tests**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit tests/unit/components/setting/SettingFeedSourcePanel.spec.ts
```

Expected: all `SettingFeedSourcePanel` tests pass.

### Task 4: API Documentation And Full Verification

**Files:**
- Modify: `Atoman-Backend/docs/docs.go`
- Modify: `Atoman-Backend/docs/swagger.json`
- Modify: `Atoman-Backend/docs/swagger.yaml`

- [ ] **Step 1: Regenerate backend Swagger docs**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go generate ./cmd/start_server
```

Expected: generated Swagger files include `/api/v1/feed/sources/opml/export`.

- [ ] **Step 2: Verify docs mention both admin OPML routes**

Run:

```bash
cd /root/Atoman/Atoman-Backend
rg -n '/api/v1/feed/sources/opml/(import|export)' docs/docs.go docs/swagger.json docs/swagger.yaml
```

Expected: import and export paths are present.

- [ ] **Step 3: Run backend verification**

Run:

```bash
cd /root/Atoman/Atoman-Backend
go test ./internal/modules/feed -run 'Test(ImportGlobalOPML|ExportGlobalOPML)' -count=1
go build ./...
```

Expected: selected backend tests pass and the full backend build succeeds.

- [ ] **Step 4: Run frontend verification**

Run:

```bash
cd /root/Atoman/Atoman-Frontend
bun run test:unit tests/unit/components/setting/SettingFeedSourcePanel.spec.ts
bun run type-check
```

Expected: component tests pass and type-check succeeds.

- [ ] **Step 5: Review final diff scope**

Run:

```bash
cd /root/Atoman
git -C Atoman-Backend status --short
git -C Atoman-Frontend status --short
```

Expected: OPML-related changes are visible, while unrelated pre-existing changes remain unstaged and untouched.
