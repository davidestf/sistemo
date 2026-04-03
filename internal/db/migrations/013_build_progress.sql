-- Add progress tracking columns to image_build for live build updates.
ALTER TABLE image_build ADD COLUMN progress INTEGER NOT NULL DEFAULT 0;
ALTER TABLE image_build ADD COLUMN progress_msg TEXT NOT NULL DEFAULT '';
