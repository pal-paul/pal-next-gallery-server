# Dynamic Personal Photo moments Generator — Requirements

## 1. Overview

Build a **generic personal photo organization application** that automatically turns a user's everyday photos into meaningful momentss.

The application is intended for photos taken with mobile phones, digital cameras, mirrorless/DSLR cameras, action cameras, scanned/imported photos, and other personal photo sources.

The primary design principle is:

> **Use traditional computer vision and deterministic algorithms for the majority of processing, and use AI only where semantic understanding provides clear additional value.**

The application should be suitable for privacy-conscious users who may want to run the complete system locally on a NAS or personal server.

---

## 2. Goals

### Primary goals

1. Import photos from one or more sources.
2. Detect exact and near-duplicate photos.
3. Extract technical and visual features.
4. Identify visually and temporally related photos.
5. Automatically group photos into moments candidates.
6. Select representative/cover photos.
7. Generate useful moments titles and descriptions.
8. Preserve original photos.
9. Process only new or changed photos during normal runs.
10. Support scheduled/background processing.
11. Minimize data sent to external AI services.
12. Allow completely local processing where possible.

### Secondary goals

Support future momentss around:

- Trips and vacations
- Family events
- Birthdays and celebrations
- Nature and outdoor photography
- Food and restaurants
- Pets
- Hobbies
- Daily-life photos
- Seasons
- Camera/photo-shoot sessions
- Locations
- Dates
- User-created collections

---

## 3. Non-Goals

The initial version does not attempt to:

- Replace professional photo-editing software.
- Automatically modify original photos.
- Provide professional RAW development.
- Require facial recognition.
- Automatically publish photos to social media.
- Upload the entire photo library to a third-party AI service.
- Require AI for basic photo organization.

AI is an optional enhancement, not a hard dependency.

---

## 4. High-Level Architecture

```text
                    ┌──────────────────────────┐
                    │       Photo Sources      │
                    │                          │
                    │ Phone / Camera / SD Card │
                    │ NAS Folder / Import      │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │   Photo Ingestion        │
                    │                          │
                    │ File discovery           │
                    │ Metadata extraction      │
                    │ Hash calculation         │
                    │ Validation                │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │    Image Analysis        │
                    │                          │
                    │ OpenCV / Go              │
                    │ pHash                    │
                    │ Dimensions               │
                    │ Color / brightness       │
                    │ Visual features          │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │ Duplicate Detection      │
                    │ Exact + near duplicates  │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │ Photo Grouping            │
                    │ Similarity + clustering   │
                    │ Time + optional location │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │ moments Generator           │
                    │ Representative photos    │
                    │ Deterministic metadata    │
                    └────────────┬─────────────┘
                                 │
                           Optional AI
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │ Vision Language Model    │
                    │ Title / description      │
                    │ Semantic attributes      │
                    └────────────┬─────────────┘
                                 │
                                 ▼
                    ┌──────────────────────────┐
                    │ PostgreSQL               │
                    │ Photos / features        │
                    │ momentss / membership      │
                    │ AI metadata              │
                    └──────────────────────────┘
```

---

## 5. Core Design Principle

Use a layered architecture.

### Layer 1 — File and metadata processing

No AI:

- File discovery
- EXIF extraction
- Date/time
- Camera information
- GPS metadata
- Image dimensions
- File hash

### Layer 2 — Computer vision

No generative AI:

- Perceptual hash
- Color analysis
- Brightness
- Contrast
- Image similarity
- Feature descriptors
- Visual clustering

### Layer 3 — moments generation

Mostly deterministic:

- Group photos
- Group by time/location where available
- Select representative images
- Generate basic titles

### Layer 4 — Optional AI enrichment

Use a VLM for:

- Semantic moments titles
- Descriptions
- Scene/activity classification
- General visual themes

---

## 6. Photo Ingestion

### Supported input

The first version should support filesystem-based ingestion.

Example:

```text
/photos/
    2026/
        01/
        02/
        03/
```

Potential future sources:

- iPhone exports
- Android exports
- SD cards
- Camera storage
- SMB/NFS folders
- NAS photo libraries
- Cloud photo exports

### Supported formats

Initial:

- JPEG/JPG
- PNG
- WebP
- HEIC/HEIF

Future:

- RAW
- CR2
- CR3
- NEF
- ARW
- DNG

The original file must never be modified.

---

## 7. Photo Identification

Each photo receives a stable internal identifier.

Recommended fields:

```text
photo_id
file_hash
perceptual_hash
source_path
```

### Cryptographic hash

Use SHA-256 for exact duplicate detection:

```text
SHA256(photo bytes)
```

### Perceptual hash

Use pHash/dHash/aHash for visually similar images.

This can detect:

- Resized copies
- Recompressed copies
- Slightly modified images
- Exported copies
- Similar burst photos

---

## 8. Image Analysis

Each photo should have a reusable analysis record.

### Technical features

```text
width
height
aspect_ratio
file_size
format
orientation
creation_time
camera_make
camera_model
lens
iso
exposure
gps_latitude
gps_longitude
```

Not every camera provides all fields.

### Visual features

Calculate:

```text
brightness
contrast
saturation
dominant_colors
color_histogram
edge_density
sharpness
```

### Feature descriptors

Potential OpenCV features:

- ORB
- SIFT where deployment/licensing requirements permit
- Local feature descriptors

---

## 9. Duplicate Detection

Duplicate detection should happen before moments clustering.

### Exact duplicate

```text
SHA-256(A) == SHA-256(B)
```

Result:

```text
EXACT_DUPLICATE
```

### Near duplicate

Use perceptual-hash Hamming distance:

```text
Hamming distance <= configurable threshold
```

Result:

```text
NEAR_DUPLICATE
```

### Duplicate policy

The system must **not automatically delete photos**.

Instead, create duplicate groups:

```text
duplicate group
 ├── primary candidate
 ├── duplicate
 └── duplicate
```

The user decides what to delete/archive.

---

## 10. Photo Similarity

No single feature is sufficient for general photo grouping.

A similarity score can combine:

```text
visual similarity
color similarity
pHash similarity
temporal similarity
location similarity
```

Example starting weights:

```text
visual similarity       40%
color similarity        15%
pHash similarity        20%
time proximity          15%
location proximity      10%
```

These values should be configurable and tuned using real photo collections.

---

## 11. Temporal Grouping

Time is an important signal for personal photos.

Example:

```text
10:00 photo
10:02 photo
10:05 photo
10:07 photo
```

Likely one photo session.

Whereas photos taken days apart are less likely to belong to the same event.

Calculate:

```text
time_gap
session_id
```

An initial configurable session gap can be **30 minutes**.

---

## 12. Location Grouping

If GPS metadata exists, use it as an additional signal.

Calculate:

```text
latitude
longitude
location_cluster
place_name
```

Potential result:

```text
Stockholm
Gothenburg
Paris
Rome
```

Location processing should remain local by default.

External geocoding should be opt-in.

---

## 13. moments Clustering

The system should not require the user to specify the number of momentss.

Candidate approaches:

- DBSCAN
- HDBSCAN
- Hierarchical clustering
- Threshold-based connected components

### Initial recommendation

Start with a deterministic similarity graph or DBSCAN.

DBSCAN is useful because it does not require a predefined number of clusters and can identify unrelated images as noise.

---

## 14. Hybrid moments Grouping

Pure visual similarity is not enough.

Use:

```text
Visual similarity
+
Time proximity
+
Location proximity
+
Camera/session information
```

Example:

```text
09:30 beach
09:31 beach
09:33 beach
09:40 restaurant
09:41 restaurant
```

Possible momentss:

```text
Beach Morning
Lunch at Restaurant
```

---

## 15. Representative Images

Each moments should have one or more representative images.

Possible scoring:

```text
representative_score =
    sharpness
    + exposure_quality
    + uniqueness
    + similarity_to_group_center
```

Select approximately:

```text
1–3 representative photos
```

These can also be the only images normally sent to an external VLM.

This significantly reduces cost and privacy exposure.

---

## 16. moments Naming Without AI

The application must work without AI.

Example deterministic titles:

```text
July 2026 — Stockholm
August 2026 — Summer Trip
September 2026 — Family Weekend
October 2026 — Hiking
```

Inputs can include:

```text
date
date range
location
photo count
camera session
user-defined rules
```

Example:

```text
2026-07-12 + Stockholm + 84 photos

=> Stockholm — July 12, 2026
```

---

## 17. Optional AI moments Enrichment

AI is an optional semantic layer.

It can improve:

- moments title
- moments description
- Semantic category
- Activity
- Scene
- Style
- General visual theme

Example:

```json
{
  "title": "A Summer Day by the Water",
  "description": "A collection of outdoor photos showing a sunny day near the water, with relaxed scenes, nature and outdoor activities.",
  "confidence": 0.87,
  "attributes": {
    "scene": "outdoor",
    "activity": "leisure",
    "environment": "water",
    "season": "summer"
  }
}
```

The model must not invent:

- People
- Locations
- Dates
- Activities not visible
- Specific objects that cannot be reasonably identified

---

## 18. AI Model

The AI layer must be replaceable.

Potential models:

- Qwen vision-language models
- Gemma multimodal models
- Other open-source VLMs
- Managed cloud vision/multimodal models

A lightweight model such as Qwen3-VL 4B can be evaluated for the first implementation.

Use an abstraction such as:

```go
type momentsAI interface {
    GeneratemomentsMetadata(
        context momentsContext,
    ) (momentsMetadata, error)
}
```

This prevents the application from becoming coupled to one model/provider.

---

## 19. Local AI vs Cloud AI

Support two deployment modes.

### Local AI

```text
NAS
 ├── Photo storage
 ├── PostgreSQL
 ├── Go application
 ├── OpenCV
 └── VLM
```

Advantages:

- Maximum privacy
- No external photo transfer
- No per-request cloud cost
- Offline operation

Disadvantages:

- Limited CPU/GPU performance
- Larger models may be difficult to run
- Longer inference times

### Optional cloud GPU

For a NAS without a suitable GPU:

```text
Personal NAS
    │
    ├── OpenCV analysis
    ├── Clustering
    └── 1–3 representative images/group
                │
                ▼
        Temporary GPU server
                │
              VLM
                │
             JSON
                │
                ▼
            Personal NAS
```

The GPU environment should ideally:

```text
start/create
    ↓
process batch
    ↓
return results
    ↓
stop/destroy
```

This avoids paying for an always-running GPU.

---

## 20. Privacy Requirements

Privacy is a first-class requirement.

### Default behavior

- Original photos remain local.
- Metadata remains local.
- Database remains local.
- External AI is disabled unless enabled.
- External geocoding is disabled unless enabled.
- Face recognition is disabled unless explicitly enabled.

### External AI

If cloud AI is enabled:

1. User explicitly enables it.
2. The UI explains that selected photos leave the local environment.
3. Only required representative photos are sent.
4. EXIF metadata is stripped unless required.
5. Temporary cloud copies are deleted after processing.
6. The inference server must not become permanent photo storage.
7. Processing activity should be auditable.

---

## 21. Database Model

PostgreSQL is recommended.

### photos

```sql
photos
------
id
source_path
file_name
file_hash
perceptual_hash
mime_type
file_size
width
height
orientation
created_at
imported_at
updated_at
status
```

### image_analysis

```sql
image_analysis
--------------
id
photo_id
brightness
contrast
saturation
sharpness
dominant_colors
color_histogram
feature_data
analysis_version
created_at
updated_at
```

### photo_location

```sql
photo_location
--------------
photo_id
latitude
longitude
location_cluster
place_name
```

### momentss

```sql
momentss
------
id
title
description
moments_type
confidence
start_time
end_time
location_name
image_count
status
created_at
updated_at
```

### moments_photos

```sql
moments_photos
------------
moments_id
photo_id
similarity_score
representative_score
is_representative
created_at
```

### duplicate_groups

```sql
duplicate_groups
----------------
id
duplicate_type
created_at
```

### duplicate_group_photos

```sql
duplicate_group_photos
----------------------
group_id
photo_id
similarity_score
is_primary
```

---

## 22. Processing State

Suggested photo state:

```text
IMPORTED
    ↓
ANALYZING
    ↓
ANALYZED
    ↓
DUPLICATE_CHECKED
    ↓
GROUPED
    ↓
moments_ASSIGNED
    ↓
COMPLETED
```

Failure states:

```text
ANALYSIS_FAILED
AI_FAILED
GROUPING_FAILED
```

Failed operations must be retryable.

---

## 23. Incremental Processing

Do not reprocess the complete library every week.

Normal processing should identify:

```text
new photos
changed photos
previously failed photos
momentss affected by new photos
```

Example:

```text
Existing library: 100,000 photos
New this week:         850 photos

Process:
850 new photos
+
affected existing groups
```

not:

```text
100,000 photos
```

---

## 24. Weekly Processing

The initial deployment can run once per week.

Example:

```text
Sunday 02:00
```

Workflow:

```text
1. Scan photo directories
2. Detect new files
3. Validate images
4. Extract metadata
5. Calculate hashes
6. Detect duplicates
7. Calculate visual features
8. Find candidate existing momentss
9. Cluster new photos
10. Update/create momentss
11. Select representative images
12. Generate deterministic metadata
13. Optionally call VLM
14. Validate AI response
15. Store results
16. Mark processing complete
```

The schedule should be configurable.

---

## 25. Periodic Full Re-Clustering

Incremental processing is the default.

A periodic full reconciliation should also be supported.

Example:

```text
Weekly:
    Incremental processing

Monthly:
    Full clustering/reconciliation
```

This helps correct moments boundaries as the collection grows.

---

## 26. Recommended Technology Stack

### Backend

```text
Go
```

### Image processing

```text
OpenCV
GoCV
```

### Database

```text
PostgreSQL
```

Optional future semantic search:

```text
pgvector
```

### Storage

```text
Local filesystem
NAS
S3-compatible storage
```

### AI

```text
Qwen VLM
Gemma multimodal model
Other VLM
```

### Deployment

```text
Docker
Synology Container Manager
cron / Synology Task Scheduler
```

---

## 27. Synology NAS Deployment

A Synology NAS can be the primary private server.

Example:

```text
Synology NAS
│
├── /photos
│
├── /app
│   ├── photo-organizer
│   ├── postgres
│   └── optional-vlm
│
├── /database
│
└── /processing
```

The NAS handles:

- Photo storage
- Ingestion
- OpenCV processing
- Clustering
- PostgreSQL
- Scheduling

AI can run locally or on a temporary cloud GPU.

---

## 28. Go Application Structure

Recommended logical modules:

```text
cmd/
    server/
    worker/

internal/
    ingestion/
    metadata/
    hashing/
    imageanalysis/
    duplicate/
    similarity/
    clustering/
    momentss/
    ai/
    storage/
    database/
    scheduler/
```

Example interfaces:

```go
type ImageAnalyzer interface {
    Analyze(ctx context.Context, photo Photo) (Analysis, error)
}

type DuplicateDetector interface {
    FindDuplicates(ctx context.Context, photo Photo) ([]Photo, error)
}

type momentsClusterer interface {
    Cluster(ctx context.Context, photos []Photo) ([]momentsGroup, error)
}

type momentsAI interface {
    GeneratemomentsMetadata(
        ctx context.Context,
        moments momentsContext,
    ) (momentsMetadata, error)
}
```

---

## 29. API

Future web/mobile API:

```text
POST   /photos/import
GET    /photos
GET    /photos/:id

GET    /momentss
GET    /momentss/:id
PATCH  /momentss/:id

GET    /duplicates
POST   /duplicates/:id/ignore
POST   /duplicates/:id/delete

POST   /processing/run
GET    /processing/status
```

The MVP can initially use a CLI worker without a sophisticated UI.

---

## 30. moments Lifecycle

Suggested lifecycle:

```text
DETECTED
    ↓
GENERATED
    ↓
REVIEWED
    ↓
PUBLISHED
```

Or:

```text
DRAFT
PUBLISHED
ARCHIVED
```

Users can manually modify:

- Title
- Description
- moments membership
- Cover image
- Visibility
- Status

AI-generated metadata must never overwrite user-edited metadata without explicit permission.

---

## 31. User Control

Automation should assist the user rather than take control.

Users should be able to:

- Rename momentss
- Merge momentss
- Split momentss
- Remove photos from momentss
- Add photos to momentss
- Select cover images
- Ignore duplicate suggestions
- Delete momentss without deleting photos
- Disable AI
- Disable cloud processing
- Configure processing frequency

---

## 32. Future Extensions

### People

Optional face detection and face clustering.

This must be opt-in because biometric information is privacy-sensitive.

### Places

Automatic location grouping:

```text
Stockholm
Paris
Rome
Barcelona
```

### Activities

Possible semantic categories:

```text
Hiking
Swimming
Cycling
Cooking
Travel
Shopping
Celebration
Sports
Nature
```

### Events

Automatic event detection:

```text
Birthday
Wedding
Christmas
New Year
Concert
Graduation
Holiday
```

### Timeline

Create a chronological view:

```text
2026
 ├── January
 ├── February
 ├── March
 └── ...
```

### Smart Search

Future queries:

```text
photos from my summer vacation
photos of mountains
photos taken at the beach
photos from Paris
photos with a sunset
```

This can use image embeddings and PostgreSQL/pgvector.

### Photo Quality Detection

Identify:

- Blurry photos
- Very dark photos
- Overexposed photos
- Closed eyes
- Poor framing
- Duplicate burst photos

The system should suggest rather than automatically delete photos.

### Mobile Application

A future mobile app could:

- Upload photos
- Display generated momentss
- Approve/reject moments suggestions
- Manage duplicates
- Trigger processing

### Desktop Application

A desktop client could support:

- Local folder import
- SD-card import
- moments management
- Local processing
- Offline operation

### Photo Library Integration

Potential future integrations:

- Apple Photos
- Google Photos
- Synology Photos
- Immich
- Other photo-management systems

All integrations should be optional.

---

## 33. Security

Secure-by-default requirements:

- No public database exposure.
- No public PostgreSQL port.
- Authentication for web APIs.
- Authorization for photo access.
- TLS for remote access.
- Secrets outside source code.
- Short-lived cloud credentials.
- Short-lived upload/download URLs where applicable.
- Audit logs for cloud AI processing.
- Configurable processing-log retention.

---

## 34. Performance Requirements

Initial target:

```text
10,000–100,000 photos
```

Future target:

```text
500,000+ photos
```

Processing should be:

- Incremental
- Parallel where safe
- Restartable
- Idempotent
- Observable

The worker must avoid loading the complete photo library into memory.

---

## 35. Observability

Expose metrics such as:

```text
photos discovered
photos processed
photos failed
duplicates found
momentss created
momentss updated
AI requests
AI failures
processing duration
```

Example:

```text
Weekly processing

Photos discovered:       843
New photos:              812
Duplicates:               31
momentss updated:           14
New momentss:                7
AI requests:              21
Processing time:       18 min
```

---

## 36. Cost Optimization

The default architecture should minimize AI usage.

Instead of:

```text
1,000 photos
→ 1,000 AI requests
```

use:

```text
1,000 photos
→ OpenCV analysis
→ 20 groups
→ 1–3 representative photos/group
→ ~20 AI requests
```

AI should normally run only when:

- A new moments is created.
- An existing moments changes significantly.
- The user explicitly requests regeneration.
- AI metadata is missing.

---

## 37. Privacy-First Cloud GPU Strategy

If the NAS does not have enough compute for the VLM:

```text
NAS
 │
 ├── Original photos remain local
 ├── OpenCV analysis
 ├── Clustering
 └── Select representative images
          │
          ▼
    Temporary cloud GPU
          │
          ├── VLM inference
          ├── JSON result
          └── temporary files deleted
          │
          ▼
          NAS
```

The cloud GPU is an inference worker, not the primary photo-storage system.

---

## 38. MVP Roadmap

### Phase 1 — Core photo analysis

Implement:

- Filesystem scanning
- JPEG/PNG/HEIC support
- SHA-256
- pHash
- EXIF extraction
- Dimensions
- Basic OpenCV features
- Duplicate detection
- Similarity scoring
- Time-based grouping
- Basic clustering
- PostgreSQL persistence
- CLI worker

No AI required.

### Phase 2 — moments generation

Add:

- Representative image selection
- moments generation
- Deterministic titles
- Weekly scheduler
- Incremental processing
- moments management API

### Phase 3 — AI enrichment

Add:

- VLM
- AI-generated titles
- AI-generated descriptions
- Semantic attributes
- Local VLM deployment
- Optional cloud GPU deployment

### Phase 4 — Advanced intelligence

Add:

- Location clustering
- Semantic search
- Face/person grouping
- Mobile/desktop UI
- External photo-library integrations

---

## 39. Example End-to-End Scenario

A user returns from a vacation and copies:

```text
1,250 photos
```

into the configured photo directory.

The weekly worker starts.

### Step 1 — Discovery

```text
1,250 new files
```

### Step 2 — Analysis

OpenCV extracts:

```text
resolution
aspect ratio
brightness
colors
sharpness
pHash
visual descriptors
```

### Step 3 — Duplicate detection

```text
87 exact duplicates
143 near duplicates
```

Nothing is automatically deleted.

### Step 4 — Temporal grouping

Possible sessions:

```text
Airport
Hotel
Beach
Restaurant
Hiking
City sightseeing
```

### Step 5 — Visual clustering

The system identifies approximately:

```text
12 moments candidates
```

### Step 6 — Representative selection

Each moments receives:

```text
1–3 representative photos
```

### Step 7 — Deterministic metadata

Example:

```text
Date: 2026-08-14
Location: Barcelona
Photos: 146
```

### Step 8 — Optional AI

Only representative images are sent to the VLM.

Possible result:

```json
{
  "title": "A Day Exploring Barcelona",
  "description": "Photos from a day exploring the city, including streets, architecture and outdoor scenes.",
  "confidence": 0.89
}
```

### Step 9 — Final moments

```text
A Day Exploring Barcelona

146 photos

[cover image]
```

The original 1,250 photos remain untouched.

---

## 40. Key Architectural Principles

### 1. Local-first

The user's photos belong to the user.

### 2. AI-optional

The application remains useful without AI.

### 3. Deterministic where possible

Use algorithms instead of AI where reliable non-AI solutions exist.

### 4. Incremental

Never process the complete library unnecessarily.

### 5. Non-destructive

Never automatically modify or delete original photos.

### 6. Explainable

moments grouping should be explainable through:

```text
time
location
visual similarity
image features
```

### 7. Replaceable AI

Do not tightly couple the application to one model/provider.

### 8. Privacy by default

External processing requires explicit configuration.

### 9. User controlled

Automation provides suggestions rather than irreversible actions.

### 10. Scalable

The architecture should work for thousands of photos initially and hundreds of thousands later.

---

## 41. Recommended Initial Architecture

```text
                 ┌────────────────────┐
                 │ Personal Photos     │
                 │ Phone / Camera      │
                 └─────────┬──────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Synology NAS       │
                 │                    │
                 │ Photo Storage      │
                 │ Go Worker          │
                 │ OpenCV / GoCV      │
                 │ PostgreSQL         │
                 └─────────┬──────────┘
                           │
                     Representative
                         Images
                           │
                  ┌────────▼─────────┐
                  │ Optional VLM     │
                  │                  │
                  │ Local NAS or     │
                  │ Temporary GPU    │
                  └────────┬─────────┘
                           │
                           ▼
                 ┌────────────────────┐
                 │ Personal momentss    │
                 │                    │
                 │ Title              │
                 │ Description        │
                 │ Cover              │
                 │ Photos             │
                 └────────────────────┘
```

---

## 42. Success Criteria

The MVP is successful when a user can:

1. Copy photos from a phone or camera into a configured folder.
2. Run the photo organizer.
3. Identify duplicates without deleting anything.
4. Group visually/time-related photos automatically.
5. See generated moments candidates.
6. See representative cover photos.
7. Generate useful titles without AI.
8. Optionally generate better titles/descriptions using a VLM.
9. Run processing automatically every week.
10. Keep the original photo library under their control.
11. Process only new/changed photos after the initial scan.
12. Disable all external AI processing if desired.

---

## 43. Long-Term Vision

The long-term goal is a **private personal photo intelligence system**, rather than simply a duplicate finder.

```text
                    Personal Photo Library
                             │
             ┌───────────────┼────────────────┐
             │               │                │
           Time            Place            Visual
             │               │                │
          Events           Trips            Scenes
             │               │                │
             └───────────────┼────────────────┘
                             │
                       Smart momentss
                             │
                    ┌────────┼────────┐
                    │        │        │
                 Search   Timeline  Stories
```

The system should remain:

- Privacy-first
- User-controlled
- Non-destructive
- Useful without AI
- Extensible with AI
- Suitable for local NAS/private-server deployment

AI should act as an optional semantic layer on top of a strong deterministic photo-processing foundation.
