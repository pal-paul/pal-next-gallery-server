import base64
import binascii
import io
import os
from contextlib import asynccontextmanager

import torch
from fastapi import FastAPI, HTTPException
from PIL import Image, UnidentifiedImageError
from pydantic import BaseModel
from transformers import CLIPModel, CLIPProcessor

MODEL_ID = os.getenv("EMBEDDING_MODEL", "openai/clip-vit-base-patch32")
MODEL_REVISION = os.getenv("EMBEDDING_MODEL_REVISION", "main")

model: CLIPModel | None = None
processor: CLIPProcessor | None = None


class EmbeddingRequest(BaseModel):
    model: str
    image: str


class EmbeddingResponse(BaseModel):
    model: str
    version: str
    embedding: list[float]


@asynccontextmanager
async def lifespan(_: FastAPI):
    global model, processor
    processor = CLIPProcessor.from_pretrained(MODEL_ID, revision=MODEL_REVISION)
    model = CLIPModel.from_pretrained(MODEL_ID, revision=MODEL_REVISION)
    model.eval()
    yield


app = FastAPI(lifespan=lifespan)


@app.get("/healthz")
def health() -> dict[str, str]:
    if model is None or processor is None:
        raise HTTPException(status_code=503, detail="model is not loaded")
    return {"status": "ok"}


@app.post("/embed", response_model=EmbeddingResponse)
def embed(request: EmbeddingRequest) -> EmbeddingResponse:
    if request.model != MODEL_ID:
        raise HTTPException(status_code=400, detail=f"model must be {MODEL_ID}")
    if model is None or processor is None:
        raise HTTPException(status_code=503, detail="model is not loaded")
    try:
        image_bytes = base64.b64decode(request.image, validate=True)
        image = Image.open(io.BytesIO(image_bytes)).convert("RGB")
    except (binascii.Error, UnidentifiedImageError, ValueError) as error:
        raise HTTPException(status_code=400, detail="image must be valid base64 image data") from error

    inputs = processor(images=image, return_tensors="pt")
    with torch.inference_mode():
        features = model.get_image_features(**inputs)
        features = torch.nn.functional.normalize(features, dim=-1)

    return EmbeddingResponse(
        model=MODEL_ID,
        version=str(getattr(model.config, "_commit_hash", None) or MODEL_REVISION),
        embedding=features[0].tolist(),
    )
