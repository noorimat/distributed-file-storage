cat > scripts/init.sql << 'EOF'
-- Files table: stores metadata about uploaded files
CREATE TABLE IF NOT EXISTS files (
    file_id UUID PRIMARY KEY,
    file_name VARCHAR(255) NOT NULL,
    file_size BIGINT NOT NULL,
    encrypted BOOLEAN DEFAULT FALSE,
    salt VARCHAR(64),
    uploaded_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Chunks table: stores information about unique chunks
CREATE TABLE IF NOT EXISTS chunks (
    chunk_hash VARCHAR(64) PRIMARY KEY,
    chunk_size INTEGER NOT NULL,
    ref_count INTEGER DEFAULT 0,
    storage_path VARCHAR(512) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- File_chunks junction table: maps files to their chunks
CREATE TABLE IF NOT EXISTS file_chunks (
    id SERIAL PRIMARY KEY,
    file_id UUID REFERENCES files(file_id) ON DELETE CASCADE,
    chunk_hash VARCHAR(64) REFERENCES chunks(chunk_hash) ON DELETE CASCADE,
    chunk_order INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(file_id, chunk_order)
);

-- Indexes for performance
CREATE INDEX IF NOT EXISTS idx_files_uploaded_at ON files(uploaded_at DESC);
CREATE INDEX IF NOT EXISTS idx_chunks_ref_count ON chunks(ref_count);
CREATE INDEX IF NOT EXISTS idx_file_chunks_file_id ON file_chunks(file_id);
CREATE INDEX IF NOT EXISTS idx_file_chunks_chunk_hash ON file_chunks(chunk_hash);

-- Function to update updated_at timestamp
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = CURRENT_TIMESTAMP;
    RETURN NEW;
END;
$$ language 'plpgsql';

-- Trigger to auto-update updated_at
CREATE TRIGGER update_files_updated_at BEFORE UPDATE ON files
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();
EOF