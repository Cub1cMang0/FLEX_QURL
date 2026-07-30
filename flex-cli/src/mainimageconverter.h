#ifndef MAINIMAGECONVERTER_H
#define MAINIMAGECONVERTER_H

#include <QImage>
#include "imagefileconverter.h"
#include "json.hpp"

using json = nlohmann::json;

class MainImageConverter: public QObject, public ImageFileConverter
{
    Q_OBJECT
    public:
        explicit MainImageConverter(QObject *parent = nullptr);
        void convert_image(const QString &input_path, const QString &output_path, const QString &input_ext, const QString &output_ext, const json &load_data);
    signals:
        void update_image_progress(const QString &message, bool success);
};

#endif
