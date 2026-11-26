package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"plugin"
	"time"

	"github.com/labstack/echo/v4"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ListPlugins returns all plugins with optional filters
func (pu *PluginUploader) ListPlugins(c echo.Context) error {
	ctx := context.Background()

	// Build filter
	filter := bson.M{}

	// Filter by enabled status
	if enabled := c.QueryParam("enabled"); enabled != "" {
		switch enabled {
		case "true":
			filter["enabled"] = true
		case "false":
			filter["enabled"] = false
		}
	}

	// Filter by name
	if name := c.QueryParam("name"); name != "" {
		filter["name"] = bson.M{"$regex": name, "$options": "i"}
	}

	// Filter by status
	if status := c.QueryParam("status"); status != "" {
		filter["status"] = status
	}

	// Query options
	opts := options.Find().SetSort(bson.D{{Key: "uploaded_at", Value: -1}})

	cursor, err := pu.collection.Find(ctx, filter, opts)
	if err != nil {
		pu.logger.WithError(err).Error("Failed to list plugins")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugins",
		})
	}
	defer cursor.Close(ctx)

	var plugins []PluginInfo
	if err := cursor.All(ctx, &plugins); err != nil {
		pu.logger.WithError(err).Error("Failed to decode plugins")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugins",
		})
	}

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"plugins": plugins,
		"count":   len(plugins),
	})
}

// GetPlugin returns a single plugin by ID
func (pu *PluginUploader) GetPlugin(c echo.Context) error {
	ctx := context.Background()

	// Get plugin ID from path
	idStr := c.Param("id")
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid plugin ID",
		})
	}

	var plugin PluginInfo
	err = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&plugin)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return c.JSON(404, map[string]interface{}{
				"error": "Plugin not found",
			})
		}
		pu.logger.WithError(err).Error("Failed to get plugin")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugin",
		})
	}

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"plugin":  plugin,
	})
}

// EnablePlugin enables a plugin and loads it into the middleware chain
func (pu *PluginUploader) EnablePlugin(c echo.Context, pm *PluginManager) error {
	ctx := context.Background()

	// Get plugin ID
	idStr := c.Param("id")
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid plugin ID",
		})
	}

	// Get plugin info
	var pluginInfo PluginInfo
	err = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return c.JSON(404, map[string]interface{}{
				"error": "Plugin not found",
			})
		}
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugin",
		})
	}

	// Check if already enabled
	if pluginInfo.Enabled {
		return c.JSON(200, map[string]interface{}{
			"success": true,
			"message": "Plugin is already enabled",
			"plugin":  pluginInfo,
		})
	}

	if err := pu.activate(ctx, &pluginInfo, pm); err != nil {
		pu.markError(ctx, id, err)
		status := 500
		if errors.Is(err, errInvalidPlugin) {
			status = 400
		}
		return c.JSON(status, map[string]interface{}{"error": err.Error()})
	}

	// Update plugin status
	now := time.Now()
	update := bson.M{
		"$set": bson.M{
			"enabled":         true,
			"status":          "active",
			"last_enabled_at": now,
			"updated_at":      now,
			"error_message":   "",
		},
		"$inc": bson.M{
			"usage_count": 1,
		},
	}

	_, err = pu.collection.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		pu.logger.WithError(err).Warn("Failed to update plugin status")
	}

	pu.logger.WithField("plugin", pluginInfo.Name).Info("Plugin enabled successfully")

	// Get updated plugin info
	_ = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo) // best effort refresh

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"message": "Plugin enabled successfully",
		"plugin":  pluginInfo,
	})
}

// DisablePlugin disables a plugin and removes it from the middleware chain
func (pu *PluginUploader) DisablePlugin(c echo.Context, pm *PluginManager) error {
	ctx := context.Background()

	// Get plugin ID
	idStr := c.Param("id")
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid plugin ID",
		})
	}

	// Get plugin info
	var pluginInfo PluginInfo
	err = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return c.JSON(404, map[string]interface{}{
				"error": "Plugin not found",
			})
		}
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugin",
		})
	}

	// Check if already disabled
	if !pluginInfo.Enabled {
		return c.JSON(200, map[string]interface{}{
			"success": true,
			"message": "Plugin is already disabled",
			"plugin":  pluginInfo,
		})
	}

	// Unregister from middleware chain
	if err := pm.UnregisterMiddleware(pluginInfo.Name); err != nil {
		pu.logger.WithError(err).Warn("Failed to unregister middleware")
	}

	// Update plugin status
	update := bson.M{
		"$set": bson.M{
			"enabled":    false,
			"status":     "disabled",
			"updated_at": time.Now(),
		},
	}

	_, err = pu.collection.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		pu.logger.WithError(err).Error("Failed to update plugin status")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to disable plugin",
		})
	}

	pu.logger.WithField("plugin", pluginInfo.Name).Info("Plugin disabled successfully")

	// Get updated plugin info
	_ = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo) // best effort refresh

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"message": "Plugin disabled successfully",
		"plugin":  pluginInfo,
	})
}

// DeletePlugin deletes a plugin and its associated file
func (pu *PluginUploader) DeletePlugin(c echo.Context, pm *PluginManager) error {
	ctx := context.Background()

	// Get plugin ID
	idStr := c.Param("id")
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid plugin ID",
		})
	}

	// Get plugin info
	var pluginInfo PluginInfo
	err = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return c.JSON(404, map[string]interface{}{
				"error": "Plugin not found",
			})
		}
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to retrieve plugin",
		})
	}

	// If plugin is enabled, disable it first
	if pluginInfo.Enabled {
		if err := pm.UnregisterMiddleware(pluginInfo.Name); err != nil {
			pu.logger.WithError(err).Warn("Failed to unregister middleware before deletion")
		}
	}

	// Delete the file from GridFS
	if err := pu.bucket.Delete(pluginInfo.FileID); err != nil {
		pu.logger.WithError(err).Warn("Failed to delete file from GridFS")
	}

	// Delete plugin metadata
	_, err = pu.collection.DeleteOne(ctx, bson.M{"_id": id})
	if err != nil {
		pu.logger.WithError(err).Error("Failed to delete plugin")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to delete plugin",
		})
	}

	pu.logger.WithField("plugin", pluginInfo.Name).Info("Plugin deleted successfully")

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"message": "Plugin deleted successfully",
	})
}

// UpdatePluginConfig updates the configuration of a plugin
func (pu *PluginUploader) UpdatePluginConfig(c echo.Context) error {
	ctx := context.Background()

	// Get plugin ID
	idStr := c.Param("id")
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid plugin ID",
		})
	}

	// Parse request body
	var req struct {
		Config map[string]interface{} `json:"config"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(400, map[string]interface{}{
			"error": "Invalid request body",
		})
	}

	// Update plugin config
	update := bson.M{
		"$set": bson.M{
			"config":     req.Config,
			"updated_at": time.Now(),
		},
	}

	result, err := pu.collection.UpdateOne(ctx, bson.M{"_id": id}, update)
	if err != nil {
		pu.logger.WithError(err).Error("Failed to update plugin config")
		return c.JSON(500, map[string]interface{}{
			"error": "Failed to update plugin configuration",
		})
	}

	if result.MatchedCount == 0 {
		return c.JSON(404, map[string]interface{}{
			"error": "Plugin not found",
		})
	}

	// Get updated plugin
	var pluginInfo PluginInfo
	_ = pu.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&pluginInfo) // best effort refresh

	pu.logger.WithField("plugin", pluginInfo.Name).Info("Plugin configuration updated")

	return c.JSON(200, map[string]interface{}{
		"success": true,
		"message": "Configuration updated successfully",
		"plugin":  pluginInfo,
	})
}

// downloadPluginFromGridFS writes the stored binary to a new temporary file in the private
// upload directory and returns its path.
func (pu *PluginUploader) downloadPluginFromGridFS(ctx context.Context, fileID primitive.ObjectID) (string, error) {
	downloadStream, err := pu.bucket.OpenDownloadStream(fileID)
	if err != nil {
		return "", fmt.Errorf("failed to open download stream: %w", err)
	}
	defer func() { _ = downloadStream.Close() }()

	destFile, err := os.CreateTemp(pu.uploadDir, "enable-*.so")
	if err != nil {
		return "", fmt.Errorf("failed to create destination file: %w", err)
	}
	if _, err := io.Copy(destFile, downloadStream); err != nil {
		_ = destFile.Close()
		_ = os.Remove(destFile.Name())
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	if err := destFile.Close(); err != nil {
		_ = os.Remove(destFile.Name())
		return "", fmt.Errorf("failed to download file: %w", err)
	}
	return destFile.Name(), nil
}

// verifyFileHash checks a file against the hex SHA-256 recorded at upload.
func verifyFileHash(path, want string) error {
	if want == "" {
		return fmt.Errorf("no hash recorded for this plugin")
	}
	f, err := os.Open(path) //nolint:gosec // our own temporary file
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("sha256 %s, want %s", got, want)
	}
	return nil
}

// GetPluginStats returns statistics about plugins
func (pu *PluginUploader) GetPluginStats(c echo.Context) error {
	ctx := context.Background()

	// Count total plugins
	totalCount, _ := pu.collection.CountDocuments(ctx, bson.M{})

	// Count enabled plugins
	enabledCount, _ := pu.collection.CountDocuments(ctx, bson.M{"enabled": true})

	// Count disabled plugins
	disabledCount, _ := pu.collection.CountDocuments(ctx, bson.M{"enabled": false})

	// Count plugins by status
	pipeline := []bson.M{
		{"$group": bson.M{
			"_id":   "$status",
			"count": bson.M{"$sum": 1},
		}},
	}

	cursor, _ := pu.collection.Aggregate(ctx, pipeline)
	defer cursor.Close(ctx)

	statusCounts := make(map[string]int)
	for cursor.Next(ctx) {
		var result struct {
			ID    string `bson:"_id"`
			Count int    `bson:"count"`
		}
		if err := cursor.Decode(&result); err == nil {
			statusCounts[result.ID] = result.Count
		}
	}

	return c.JSON(200, map[string]interface{}{
		"success":       true,
		"total_plugins": totalCount,
		"enabled":       enabledCount,
		"disabled":      disabledCount,
		"status_counts": statusCounts,
	})
}

// errInvalidPlugin marks activation failures caused by the plugin itself (a 400 for the admin).
var errInvalidPlugin = errors.New("invalid plugin")

// activate downloads an uploaded plugin, checks it is the binary that was uploaded (SHA-256)
// and still valid, opens it and puts its middleware on the request path. Opening runs the
// plugin's code, so it happens only here: on an explicit enable, or at startup for plugins
// that were enabled.
func (pu *PluginUploader) activate(ctx context.Context, info *PluginInfo, pm *PluginManager) error {
	if err := ValidatePluginName(info.Name); err != nil {
		return fmt.Errorf("%w: %v", errInvalidPlugin, err)
	}
	tempPath, err := pu.downloadPluginFromGridFS(ctx, info.FileID)
	if err != nil {
		return fmt.Errorf("failed to load plugin file: %w", err)
	}
	defer func() { _ = os.Remove(tempPath) }()

	if err := verifyFileHash(tempPath, info.FileHash); err != nil {
		return fmt.Errorf("plugin binary does not match the uploaded file, refusing to load it: %w", err)
	}
	if err := NewPluginValidator().ValidatePlugin(tempPath, info.Name); err != nil {
		return fmt.Errorf("%w: plugin validation failed: %v", errInvalidPlugin, err)
	}

	p, err := plugin.Open(tempPath)
	if err != nil {
		return fmt.Errorf("failed to load plugin: %w", err)
	}
	var middleware Middleware
	if err := safeCall(func() error {
		var rerr error
		middleware, rerr = resolveMiddleware(p, info.Config)
		return rerr
	}); err != nil {
		return fmt.Errorf("%w: %v", errInvalidPlugin, err)
	}
	if err := pm.RegisterMiddleware(info.Name, middleware, info.Metadata.Priority, info.Metadata.Routes, info.Metadata.Phase); err != nil {
		return fmt.Errorf("%w: %v", errInvalidPlugin, err)
	}
	return nil
}

func (pu *PluginUploader) markError(ctx context.Context, id primitive.ObjectID, cause error) {
	pu.logger.WithError(cause).WithField("plugin_id", id.Hex()).Error("Plugin could not be activated")
	_, _ = pu.collection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
		"status":        "error",
		"error_message": cause.Error(),
		"updated_at":    time.Now(),
	}})
}

// RestoreEnabled activates the uploaded plugins that are marked enabled, at startup. (They
// were recorded as enabled but nothing loaded them again after a restart.) A plugin that fails
// is marked with status "error" and the others still load.
func (pu *PluginUploader) RestoreEnabled(ctx context.Context, pm *PluginManager) (int, error) {
	cursor, err := pu.collection.Find(ctx, bson.M{"enabled": true})
	if err != nil {
		return 0, err
	}
	var enabled []PluginInfo
	if err := cursor.All(ctx, &enabled); err != nil {
		return 0, err
	}
	loaded := 0
	for i := range enabled {
		if err := pu.activate(ctx, &enabled[i], pm); err != nil {
			pu.markError(ctx, enabled[i].ID, err)
			continue
		}
		loaded++
	}
	return loaded, nil
}
