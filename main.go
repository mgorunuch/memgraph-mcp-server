package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Version information set by build flags
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// Driver is the global Memgraph connection driver
var Driver neo4j.DriverWithContext

// MCPConfig holds the configuration for the MCP server
type MCPConfig struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

func main() {
	// Define command line flags
	jsonFlag := flag.Bool("json", false, "Output MCP configuration as JSON")
	versionFlag := flag.Bool("version", false, "Display version information")
	connectionUriPtr := flag.String("connection-uri", "", "Memgraph connection URI (bolt://host:port)")
	usernamePtr := flag.String("username", "", "Memgraph username")
	passwordPtr := flag.String("password", "", "Memgraph password")
	flag.Parse()

	// Check if version information is requested
	if *versionFlag {
		fmt.Printf("Memgraph MCP Server\nVersion: %s\nCommit: %s\nBuild date: %s\n", version, commit, date)
		return
	}

	// Check if JSON output is requested
	if *jsonFlag {
		config := MCPConfig{
			Type:    "stdio",
			Command: getExecutablePath(),
			Args:    []string{}, // Initialize with empty slice - Claude Code requires empty array not null
			Env:     map[string]string{}, // Initialize with empty map - Claude Code requires empty object not null
		}

		// Add connection parameters as arguments if provided
		if *connectionUriPtr != "" {
			config.Args = append(config.Args, "--connection-uri", *connectionUriPtr)
		}
		if *usernamePtr != "" {
			config.Args = append(config.Args, "--username", *usernamePtr)
		}
		if *passwordPtr != "" {
			config.Args = append(config.Args, "--password", *passwordPtr)
		}

		// Output JSON configuration
		jsonOutput, err := json.Marshal(config)
		if err != nil {
			log.Fatalf("Failed to generate JSON: %v", err)
		}
		fmt.Println(string(jsonOutput))
		return
	}

	// Normal server operation mode
	// Determine connection parameters from sources in order of priority:
	// 1. Command line flags
	// 2. Environment variables
	// 3. Default values
	connectionUri := "bolt://localhost:7687"
	username := ""
	password := ""

	// Override with environment variables if provided
	if envUri := os.Getenv("MEMGRAPH_URI"); envUri != "" {
		connectionUri = envUri
	}
	if envUser := os.Getenv("MEMGRAPH_USER"); envUser != "" {
		username = envUser
	}
	if envPass := os.Getenv("MEMGRAPH_PASSWORD"); envPass != "" {
		password = envPass
	}

	// Override with command line flags if provided
	if *connectionUriPtr != "" {
		connectionUri = *connectionUriPtr
	}
	if *usernamePtr != "" {
		username = *usernamePtr
	}
	if *passwordPtr != "" {
		password = *passwordPtr
	}

	// Connect to Memgraph
	var err error
	auth := neo4j.NoAuth()
	if username != "" || password != "" {
		auth = neo4j.BasicAuth(username, password, "")
	}

	Driver, err = neo4j.NewDriverWithContext(connectionUri, auth)
	if err != nil {
		log.Fatalf("Failed to create driver: %v", err)
	}
	defer Driver.Close(context.Background())

	// Test the connection
	ctx := context.Background()
	err = Driver.VerifyConnectivity(ctx)
	if err != nil {
		log.Fatalf("Failed to connect to Memgraph: %v", err)
	}
	log.Println("Connected to Memgraph database")

	// Create a new MCP server
	s := server.NewMCPServer(
		"Memgraph MCP Server",
		version, // Version from build flags
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	// Add Memgraph query tool
	queryTool := mcp.NewTool("run_query",
		mcp.WithDescription("Execute a Cypher query against Memgraph"),
		mcp.WithString("query", mcp.Required(), mcp.Description("The Cypher query to execute")),
		mcp.WithBoolean("unsafe", mcp.Description("Set to true to allow potentially unsafe queries (use with caution)")),
	)

	// Add the query tool handler
	s.AddTool(queryTool, handleQuery)

	// Add Memgraph schema information tool
	schemaInfoTool := mcp.NewTool("get_schema",
		mcp.WithDescription("Get schema information about the Memgraph database"),
	)

	// Add the schema info tool handler
	s.AddTool(schemaInfoTool, handleSchemaInfo)

	// Start the server using stdio
	log.Println("Starting Memgraph MCP Server...")
	if err := server.ServeStdio(s); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// handleQuery executes a Cypher query and returns the result
func handleQuery(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, ok := request.Params.Arguments["query"].(string)
	if !ok {
		return mcp.NewToolResultError("Query parameter is required"), nil
	}

	// Check if unsafe is enabled
	unsafe := false
	if val, ok := request.Params.Arguments["unsafe"].(bool); ok {
		unsafe = val
	}

	// Basic safety check for non-unsafe queries
	if !unsafe {
		lowerQuery := strings.ToLower(query)
		if strings.Contains(lowerQuery, "delete ") ||
			strings.Contains(lowerQuery, "remove ") ||
			strings.Contains(lowerQuery, "drop ") ||
			strings.Contains(lowerQuery, "create ") ||
			strings.Contains(lowerQuery, "merge ") ||
			strings.Contains(lowerQuery, "set ") {
			return mcp.NewToolResultError("Potentially unsafe query detected. Set 'unsafe' to true to execute."), nil
		}
	}

	// Execute the query
	session := Driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)

	result, err := session.Run(ctx, query, map[string]any{})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Query execution failed: %v", err)), nil
	}

	// Process the results
	var rows []map[string]interface{}
	var keys []string

	// Collect all records
	for {
		record, err := result.Next(ctx)
		if err != nil {
			break // End of records or error
		}

		// Get keys if this is the first record
		if len(keys) == 0 {
			keys = record.Keys
		}

		// Convert record to map
		row := make(map[string]interface{})
		for _, key := range keys {
			value := record.Values[key]
			
			// Handle various Neo4j types
			switch v := value.(type) {
			case neo4j.Node:
				nodeMap := map[string]interface{}{
					"id":         v.ElementId,
					"labels":     v.Labels,
					"properties": v.Props,
				}
				row[key] = nodeMap
			case neo4j.Relationship:
				relMap := map[string]interface{}{
					"id":         v.ElementId,
					"type":       v.Type,
					"start":      v.StartElementId,
					"end":        v.EndElementId,
					"properties": v.Props,
				}
				row[key] = relMap
			case neo4j.Path:
				// Simplified path representation
				pathMap := map[string]interface{}{
					"nodes":         len(v.Nodes),
					"relationships": len(v.Relationships),
				}
				row[key] = pathMap
			default:
				row[key] = v
			}
		}
		rows = append(rows, row)
	}

	// Check for query execution errors
	if err := result.Err(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Error processing results: %v", err)), nil
	}

	// If no rows were returned but query was successful
	if len(rows) == 0 {
		jsonData := map[string]interface{}{
			"message": "Query executed successfully with no records returned",
			"columns": keys,
		}
		return mcp.NewToolResultText(fmt.Sprintf("%v", jsonData)), nil
	}

	// Return the result
	jsonData := map[string]interface{}{
		"columns": keys,
		"rows":    rows,
		"count":   len(rows),
	}
	return mcp.NewToolResultText(fmt.Sprintf("%v", jsonData)), nil
}

// handleSchemaInfo returns information about the Memgraph database schema
func handleSchemaInfo(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	session := Driver.NewSession(ctx, neo4j.SessionConfig{})
	defer session.Close(ctx)

	// Execute the schema info query
	result, err := session.Run(ctx, "SHOW SCHEMA INFO;", map[string]any{})
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Failed to get schema information: %v", err)), nil
	}

	// Process the results
	var schema []map[string]interface{}
	
	for {
		record, err := result.Next(ctx)
		if err != nil {
			break // End of records or error
		}

		schemaItem := make(map[string]interface{})
		for key, value := range record.Values {
			schemaItem[key] = value
		}
		schema = append(schema, schemaItem)
	}

	// Check for query execution errors
	if err := result.Err(); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("Error processing schema results: %v", err)), nil
	}

	// If no schema information was returned
	if len(schema) == 0 {
		return mcp.NewToolResultText("No schema information available."), nil
	}

	// Return the schema information
	jsonData := map[string]interface{}{
		"schema": schema,
	}
	return mcp.NewToolResultText(fmt.Sprintf("%v", jsonData)), nil
}

// getExecutablePath returns the full path to the current executable
func getExecutablePath() string {
	execPath, err := os.Executable()
	if err != nil {
		// Fall back to just the binary name if we can't get the path
		return filepath.Base(os.Args[0])
	}
	return execPath
}