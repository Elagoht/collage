# Faz 5 envanteri: pkg/collage'ın public API'si

Tarih: 2026-10-11, collage v0.59.0 (30f5cfa) üzerinde.

`pkg/collage`'daki her dışa açık bildirim (354 satır: 92 alias, 134 var, 38 func,
57 metot, 21 const, 12 tip) ve `Config`'in alanları tek tek gezildi. Her satırın
kullanımı 45 plugin repo'sunda, docs sitesinde, scaffold'da ve `collagetest`'te
sayıldı. Satır satır kararlar ve gerekçeler ekte:

- [A: collage.go, App, hook'lar, Host](phase5/inventory-a-collage.md) (105 satır)
- [B: page, fragment, document, action](phase5/inventory-b-routes.md) (128 satır)
- [C: hatalar, Config, build, kalanlar](phase5/inventory-c-config-errors.md) (121 satır, Config alanları)
- [Ham kullanım sayıları](phase5/usage.tsv)

## Özet

| Karar | Sayı |
|---|---|
| Kalsın (KEEP) | 285 |
| Şekli değişsin (CHANGE) | 16 + 3 Config alanı |
| Birleştirilsin (MERGE) | 9 |
| Dışa kapatılsın (UNEXPORT) | 14 |
| Kaldırılsın (REMOVE) | 8 + 3 Config alanı |
| Adı değişsin (RENAME) | 9 |
| Public pakette tanımlansın (DEFINE) | 5 |
| Soru (ASK) | 8 satır, tek konu (Inspection) |

Kırılma maliyeti düşük. Plugin'lerin üretim kodunda değişmesi gerekenler:

| Değişiklik | Etkilenen repo |
|---|---|
| `Origins` yeteneği Host'a katılır | 8: cdnpurge, feed, indexnow, meta, oauth, ogimage, robots, sitemap |
| `RouteOf` → `RouteInfo` | 2 |
| `Varied(rc)` → `Varied(ctx)`, `VariedContext` ona katılır | 2: consent, tenant |
| `DataHandler` → `LoadTagged` | 1: markdown |

Bunlara ek olarak birkaç plugin'in yalnızca testleri değişir (`JSONOf`,
`NoContent`, `WithSlot`, `WithDependency`). Scaffold'un `main.go`'su
(`NewBuilder` → `Build`) ve docs sitesi (`Config.Validate` 10 yerde) güncellenir.

## Yol haritasını değiştiren bulgu: Host zaten mühürlü

Yol haritasının ilk iki maddesi "Host'u isteğe bağlı interface'lere böl" ve
"bölünemeyenleri mühürle" idi. Gerekçe, v1'den sonra Host'a metot eklemenin
onu uygulayanları kırmasıydı.

Ama Host zaten dışarıdan uygulanamıyor: `ConfigReader`'ı gömüyor, onun tek metodu
unexported (`pluginConfig()`), ve onu karşılayan `ConfigSource` iç pakette. Dışarıdaki
bir tip Host olamaz, ancak gerçek bir Host'u gömebilir (fail2ban'ın testindeki
`useSpy{collage.Host}` gibi). Sonuçları:

- v1'den sonra Host'a metot eklemek kimseyi kırmaz. Bölmek evrim için bir şey
  kazandırmaz, yalnızca dokümanı okunur kılar.
- Type assertion ile alınan yetenekler zararlı: Host'u gömen bir sarmalayıcıda
  `host.(collage.Origins)` sessizce `false` döner ve plugin origin yokmuş gibi
  davranır. Bugün tek örneği `Origins` (8 repo).

**Öneri:** Host tek interface olarak kalır. `Origins`'in iki metodu Host'a
katılır (`OriginFor`, `Dynamic` → `MultiOrigin`). Kullanılmayan iki metot
(`RegisterPage`, `FragmentURL`, 0 çağrı) çıkar; gerekirse v1.x'te geri eklemek
bedava. Doküman metotları gruplar: ortam, rota ekleme, istek sarma, site haritası
okuma, istek anında iş. `collagetest.Host` (plugin yazarlarının test için sahte
Host'u) mühürleme için gerekmiyor; v1 sonrasına ertelenmiş "collagetest
yardımcıları" işiyle birlikte ayrıca ele alınır.

## Herkese uygulanacak kurallar

Üç inceleme ayrı ayrı aynı kalıplara vardı. Kararların çoğu bu kurallardan çıkıyor:

1. **Alias, iç tipin bütün metotlarını public yapar.** `type Page = types.Page`
   yüzünden `Root`, `Guards`, `PathFragments`, `Fragment.Bind`,
   `SetDataSource`, `DataSource` gibi yalnızca motorun kullandığı metotlar v1'de
   donar. `App` alias'ı da yalnızca `internal/build`'in çağırdığı 10 metodu
   (`RenderNotFound`, `Mounts`, `CSRFMarker`…) dondurur. Kural: alias'lanan bir
   tipin dışa açık her metodu ya kullanıcı için tasarlanmıştır ya da iç pakette
   fonksiyona çevrilir.
2. **DEFINE yalnızca kullanıcının uyguladığı, metodunu çağırdığı ya da alan alan
   doldurduğu tiplere.** `App`, `Host`, `Plugin` bugün `go doc`'ta hiç metot
   göstermiyor. `Host` ve `Plugin` için iki pakette aynı interface literal'ine
   alias verilir: tip uyumu bozulmaz, metotlar dokümanda görünür (bir deneme
   modülünde doğrulandı). `App` public pakette sarmalayıcı struct olur.
   `BuildOptions`, `BuildReport`, `SkipRecord`, `WarningRecord` de tanımlanır.
3. **Hata sabitleri üç kademe.** Çalışma anında ve ErrorHook'a ulaşanlar ile bir
   testin ayırt edeceği kayıt hataları kalır. Nil/boş argüman hataları
   (`ErrNil*`, `ErrEmpty*Name`), şablon yazım hataları (`ErrDict*`), yalnızca
   başka bir sabitin içinde sarılı gelenler ve `New` üzerinden ulaşılamayanlar
   dışa kapatılır ya da kaldırılır. Hepsi `errors.go`'da, alana göre gruplu durur.
4. **Bir olgu, tek alan ya da tek fonksiyon.** `RouteOf` ⊂ `RouteInfo`,
   `FragmentMetadata` = `FragmentReport`, `CacheInvalidateEvent.Paths` ⊂ `Entries`,
   `ErrMissingTTL` ≈ `ErrInvalidTTL`, `ErrDuplicateOutputPath` =
   `ErrOutputPathCollision`, `ErrInvalidHandlerPrefix` = `ErrInvalidPrefix`,
   `VariedContext` = `Varied`, `ErrUnsupportedCache` = `ErrInvalidCacheType`.
5. **Adlandırma tutarlılığı.**
   - Durum kodu alanının adı her yerde `Status`: `Redirect.StatusCode` ve gereksiz
     `Permanent` tek `Status`'a iner.
   - Cache tag'i alan her şeyin adında "Tags" geçer: `WithDependency` →
     `WithDependencyTags`.
   - Kapatma bayrakları `Disable<X> bool`: `NoSniff *bool` → `DisableNoSniff`.
   - Aile önekleri: `ErrNoMethods` → `ErrNoActionMethods`, `ErrInvalidOutDir` →
     `ErrEmptyOutDir`, `ErrNilRenderer` → `ErrNilApp`, `ErrInvalidBaseURL` →
     `ErrInvalidOrigin` (tenant origin'lerinde de dönüyor, mesajı BaseURL'ü suçluyor).
   - Sık durum kısa adı alır: `JSONOf` → `JSON`, bugünkü `JSON` (hazır bayt) →
     `JSONBytes`. `NoContent(401)` 204 gibi okunuyor → `Status`.
6. **Her kullanıcı callback'i önce `ctx` alır.** Tek istisna `SlotResolverFunc`
   (0 kullanıcı) düzeltilir. `GuardFunc` kalır, dokümanı nedenini söyler.
7. **Builder metotlarında çıplak bool yok.** `WithSlot(name, required,
   allowMultiple bool)` → `WithSlot(name, rules ...SlotRule)`.
8. **Fonksiyon tutan dışa açık `var` yok.** `WithCacheControl`, `WithoutBuildCopy`
   ve benzerleri `func` olur; çağıranlar etkilenmez, kimse yeniden atayamaz.
9. **Her iş için tek giriş noktası.** Varsayılanları uygulamanın ve doğrulamanın
   tek yolu `New`: `Config.Validate`, `ApplyDefaults` dışa kapatılır
   (`Config{}.Validate()` bugün varsayılanlar uygulanmadığı için `ErrInvalidPort`
   veriyor). `NewBuilder` + `Builder.Build` → `collage.Build(ctx, app, opts)`.
10. **Kullanıcısı olmayan isteğe bağlı hook'lar şimdi çıkar.** v1.x'te geri
    eklemek kimseyi kırmaz, ama dondurulunca garip anlamları kalıcı olur:
    `PageResolvedHook`/`Event`, `CacheWriteHook`/`Event` (0 uygulayan).
11. **Public godoc iç paket adı geçirmez.** CI'a `pkg/collage` yorumlarında
    `internal/` ve `types.` arayan bir kontrol eklenir.

## Diğer şekil değişiklikleri

- `Cache.Invalidate` → `TaggedCache`'e taşınır (framework yalnızca orada çağırıyor).
- `Result.HoistEnds` dışa kapatılır, `Result.FragmentReports()` çıkar.
- `Page.LayoutFragment` dışa kapatılır (kayıttan önce ve sonra farklı şey
  demek; kullanıcının tanımladığı `LayoutChain`).
- `Config`: `Template.DevMode` (ikinci bir dev anahtarı) ve
  `Locale.DisablePathLocale` (tek locale ile aynı şey) çıkar; `Cache.MaxKeysPerTag`
  çıkar (iç ayar, `MaxEntries`'e bağlanır); `Cache.Type` tipli olur (`CacheMemory`,
  `CacheDisk`; string literal'ler derlenmeye devam eder); `Config.IsDevMode` çıkar.
- `CheckCommand`, `InspectCommand` sabitleri dışa kapatılır (CLI ile program
  arasındaki tel adı).
- `AfterRenderEvent.Path` eklenir (eklemeli; diğer iki event'te var).

## Senin kararın gereken konular

1. **Host bölünmesi** (yukarıda). Öneri: bölme, `Origins`'i Host'a kat, iki
   kullanılmayan metodu çıkar.
2. **Inspection.** `App.Inspect` ve 9 `Inspected*` tipi:
   (a) public kalır, v1'de donar, eksik `InspectedType` eklenir; ya da
   (b) iç pakete taşınır, tek sözleşme `collage inspect`'in sürümlü JSON'u olur.
   Hiçbir plugin ve `collagetest` kullanmıyor; VS Code eklentisi JSON'u okuyor.
   Öneri: (b).
3. **Build sonrası builder.** `Build()` builder'ın kendi pointer'ını döndürüyor,
   yani `Build`'den sonraki bir `WithX` kayıtlı bir sayfayı değiştiriyor;
   `ActionBuilder`'da `BuildErr` yok. Öneri: `Build` bir kopya döndürsün, dört
   builder'ın hepsinde `Build` + `BuildErr` olsun.
4. **`ActionResult`'ta birden fazla gövde.** Hem `Page` hem `Body` dolu bir sonuç
   bugün sessizce `Page`'i render edip `Body`'yi atıyor. Öneri: reddedilsin
   (500 ve iki alanı adlandıran bir hata).
5. **Çakışan çıktı yolu.** Statik build'de iki sayfa aynı dosyaya düşerse build
   duruyor, iki document düşerse ikincisi sessizce atlanıyor. Öneri: ikisinde de
   build dursun, tek sabit (`ErrOutputPathCollision`).
6. **`WithPath` aynı locale'e iki kez** sessizce üzerine yazıyor, `""` locale'i
   dolaylı reddediliyor. Öneri: tekrar ve `""` kayıt hatası olsun.

## v1'den sonraya kalabilecekler (eklemeli, kırmaz)

- Sihirli string'ler için adlandırılmış sabitler (`Route.Kind`, `ErrorEvent.Stage`,
  `BuiltFile.Kind`): alanlar `string` kalır, sabitler sonra eklenir.
- `DocumentBuilder.WithTimeout` (bugün document'lar yalnızca
  `Template.Timeout` ile sınırlı).
- `collagetest.Host` ve plugin yazarları için test yardımcıları.

## Sonraki adım

Kararlar netleşince bu envanterden bir spec ve uygulama planı çıkar. Kırıcı
değişiklikler tek sürümde (v0.60.0) toplanır; etkilenen plugin'ler aynı turda
güncellenir; ardından `apidiff` kapısı CI'a eklenir.
