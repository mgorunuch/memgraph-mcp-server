# Memgraph MCP Server Notes

## Server Configuration
- Default Memgraph connection: bolt://localhost:7687
- Default credentials: username="" password=""
- Connection settings can be overridden via environment variables or command-line flags

## Available Tools
- `run_query`: Execute Cypher queries against Memgraph
- `get_schema`: Fetch Memgraph schema information

## Security Considerations
- Potentially destructive queries should be blocked by default unless `unsafe` parameter is set to true
- Destructive operations include: DELETE, REMOVE, DROP, CREATE, etc.

## MCP Protocol
- Using the mcp-go library for implementing MCP server functionality
- Server communicates via stdio following the Model Control Protocol

## Technical Notes
- Uses Neo4j-compatible Bolt driver for Memgraph connectivity
- Memgraph supports Cypher query language similar to Neo4j